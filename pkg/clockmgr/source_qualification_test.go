package clockmgr_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/require"
)

func TestSourceQualificationSnapshots(t *testing.T) {
	// Reset/remove touch shared debug state, so keep this test serial.
	m := clockmgr.Init("test", make(chan event.Event), nil, nil, nil, nil)
	set := qualificationPublisher(t, m)
	set(true)
	stats := receiveStatsSnapshot(t, m.StatsUpdatesCh())
	require.True(t, stats.SourceQualified["ptp4l.0.config"], "latest state retained even without a waiting consumer")
	require.NotEqual(t, event.PTP_LOCKED, stats.ClockStates["ptp4l.0.config"], "source qualification must not fabricate aggregate lock")
	source, ok := m.SourceQualification("ptp4l.0.config")
	require.True(t, ok)
	require.Equal(t, event.PTP4l, source.SourceType)
	require.Equal(t, clock.PTPPortSource{Port: "tr0"}, source.Source)
	require.Equal(t, "tr0", source.IFace)
	require.Empty(t, clockmgr.SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ptp4l.0.config", Source: event.TS2PHC}}).SourceQualified)
	set(false)
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	require.True(t, stats.SourceQualified["ptp4l.0.config"], "snapshot is detached")
	newSet := qualificationPublisher(t, m)
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	set(true)
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"], "superseded owner cannot republish readiness")
	newSet(true)
	require.True(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", Reset: true})
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"])
	newSet(true)
	require.False(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified["ptp4l.0.config"], "reset revokes old publisher")
	set = qualificationPublisher(t, m)
	set(true)
	m.RemoveAllClocks()
	set(true)
	require.Empty(t, receiveStatsSnapshot(t, m.StatsUpdatesCh()).SourceQualified, "removed owner cannot resurrect readiness")
	require.Empty(t, m.StatsUpdatesCh(), "removed owner cannot publish another snapshot")
}

func qualificationPublisher(t *testing.T, m *clockmgr.ClockManager) func(bool) {
	t.Helper()
	clk, err := clock.NewTBC("ptp4l.0.config", m.GetUtcOffset, &pmc.MockClient{})
	require.NoError(t, err)
	require.NoError(t, m.AddClock(clk))
	generation, err := m.ConfigureSourceQualification("ptp4l.0.config", []string{"tr0"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
	require.NoError(t, err)
	return func(qualified bool) {
		if qualified {
			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: &event.PTPSourceObservation{
				Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)}})
		}
		m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: &event.PTPSourceObservation{
			Generation: generation, ServoState: "s2", Offset: 10, SourceLost: !qualified}})
	}
}

func TestSourceQualificationEventRoundTrip(t *testing.T) {
	events := make(chan event.Event, 2)
	m := clockmgr.Init("test", events, nil, nil, nil, nil)
	qualificationPublisher(t, m)
	generation, err := m.ConfigureSourceQualification("ptp4l.0.config", []string{"tr0"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 2, DisqualifiedSamples: 1})
	require.NoError(t, err)
	role := &event.PTPSourceObservation{Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave), Reply: make(chan event.SourceQualification, 1)}
	sample := &event.PTPSourceObservation{Generation: generation, ServoState: "s2", Offset: 10, Reply: make(chan event.SourceQualification, 1)}
	wrap := func(data event.EventData) event.Event {
		return event.Event{Source: event.PTP4l, CfgName: "ptp4l.0.config", IFace: "tr0", Data: data}
	}
	events <- wrap(role)
	events <- wrap(sample)
	require.False(t, clockmgr.SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ptp4l.0.config"}}).SourceQualified["ptp4l.0.config"], "queued observations are not evaluated readiness")
	m.ProcessEvent(<-events)
	require.Equal(t, event.PTP4l, (<-role.Reply).SourceType)
	m.ProcessEvent(<-events)
	result := <-sample.Reply
	require.False(t, result.Qualified)
	require.Equal(t, clock.PTPPortSource{Port: "tr0"}, result.Source)
	m.ProcessEvent(wrap(&event.OffsetData{State: event.PTP_LOCKED, Offset: 10}))
	require.False(t, clockmgr.SnapshotStatsForTest(m, []process.StatsRequest{{ClockID: "ptp4l.0.config"}}).SourceQualified["ptp4l.0.config"], "averaged offsets must not count as fresh qualification samples")
	m.ProcessEvent(wrap(sample))
	require.True(t, (<-sample.Reply).Qualified)
	m.SetApplying(true)
	m.ProcessEvent(wrap(sample))
	require.Empty(t, (<-sample.Reply).SourceType, "suppressed input still gets a rejection reply")
	m.SetApplying(false)
	m.RemoveAllClocks()
	m.ProcessEvent(wrap(sample))
	require.Empty(t, (<-sample.Reply).SourceType, "removed clocks must not strand their old scanner")
}
