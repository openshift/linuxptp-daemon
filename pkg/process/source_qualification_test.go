package process

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/require"
)

func TestSourceQualifiedConditionUsesRetainedState(t *testing.T) {
	t.Parallel()
	c := OnSourceQualified{ConfigName: "ptp4l.0.config"}
	require.False(t, c.MetByCurrentState(nil, nil, EventStats{}))
	require.False(t, c.MetByCurrentState(nil, nil, EventStats{SourceQualified: map[string]bool{"ptp4l.1.config": true}}))
	stats := EventStats{SourceQualified: map[string]bool{"ptp4l.0.config": true}}
	require.True(t, c.MetByCurrentState(nil, nil, stats))
	require.False(t, c.Met(nil, event.Event{Source: event.PTP4l, CfgName: c.ConfigName}, EventStats{}), "raw wakeup is not readiness")
	require.False(t, c.Met(nil, event.Event{Reset: true}, stats))
	require.NotEmpty(t, c.String())
	require.Equal(t, []StatsRequest{{ClockID: c.ConfigName, Source: event.PTP4l}}, c.GetStatsRequests())
}

func TestSourceQualifiedAndPhc2sysLocked(t *testing.T) {
	t.Parallel()
	c := &All{Conditions: []Condition{
		OnSourceQualified{ConfigName: "ptp4l.0.config"},
		OnProcessState{ConfigName: "ptp4l.0.config", Source: string(event.PHC2SYS), State: event.PTP_LOCKED},
	}}
	stats := EventStats{SourceQualified: map[string]bool{"ptp4l.0.config": true}, States: map[string]map[event.EventSource]event.PTPState{}}
	running := func(string, string) bool { return true }
	require.False(t, c.MetByCurrentState(nil, running, stats), "running alone is insufficient")
	stats.States["ptp4l.0.config"] = map[event.EventSource]event.PTPState{event.PHC2SYS: event.PTP_LOCKED}
	require.True(t, c.MetByCurrentState(nil, running, stats))
	stats.SourceQualified["ptp4l.0.config"] = false
	require.False(t, c.MetByCurrentState(nil, running, stats), "must not latch previous qualification")
	stats.SourceQualified["ptp4l.0.config"] = true
	stats.States["ptp4l.0.config"][event.PHC2SYS] = event.PTP_FREERUN
	require.False(t, c.MetByCurrentState(nil, running, stats), "must not latch previous phc2sys lock")
}
