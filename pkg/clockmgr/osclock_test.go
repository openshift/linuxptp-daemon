package clockmgr_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testClockmgrPtp4lConfig = "ptp4l.0.config"

func osClockMessages(cache *ipc.Cache) []ipc.Message {
	var messages []ipc.Message
	for {
		select {
		case msg := <-cache.Out():
			if msg.Type == ipc.TypeOSClockState {
				messages = append(messages, msg)
			}
		default:
			return messages
		}
	}
}

func TestOSClock_FilteredSnapshotsAndE3(t *testing.T) {
	cache := ipc.NewCache(100)
	m := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, cache)
	m.ConfigureOSClockFilter(clock.OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 2})
	bc, err := clock.NewBC(testClockmgrPtp4lConfig, false, event.PtpClockThreshold{MinOffsetThreshold: -100, MaxOffsetThreshold: 100})
	require.NoError(t, err)
	require.NoError(t, m.AddClock(bc))
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, IFace: "eth0", Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}})
	sample := func(offset int64) {
		m.ProcessEvent(event.Event{Source: event.PHC2SYS, CfgName: testClockmgrPtp4lConfig, IFace: parserconstants.ClockRealTime, Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: offset}})
	}
	sample(10)
	stats := receiveStatsSnapshot(t, m.StatsUpdatesCh())
	assert.Equal(t, event.PTP_NOTSET, stats.States[testClockmgrPtp4lConfig][event.PHC2SYS])
	assert.Nil(t, stats.Windows[testClockmgrPtp4lConfig][event.PHC2SYS], "phc2sys readiness needs no offset window")
	assert.Equal(t, event.PTP_LOCKED, stats.ClockStates[testClockmgrPtp4lConfig])
	assert.Equal(t, event.PTP_FREERUN, stats.OSClockState)

	sample(20)
	assert.Equal(t, event.PTP_NOTSET, stats.States[testClockmgrPtp4lConfig][event.PHC2SYS], "state snapshots do not change after capture")
	stats = receiveStatsSnapshot(t, m.StatsUpdatesCh())
	assert.Equal(t, event.PTP_LOCKED, stats.States[testClockmgrPtp4lConfig][event.PHC2SYS])
	assert.Equal(t, event.PTP_LOCKED, stats.OSClockState)
	assert.Equal(t, event.PTP_LOCKED, stats.OSClockE3State)
	osClockMessages(cache)

	// A steady/jitter sample refreshes the offset, but does not emit another E3.
	sample(150)
	assert.Empty(t, osClockMessages(cache))
	stats = receiveStatsSnapshot(t, m.StatsUpdatesCh())
	_, offset := clockmgr.OSClockSampleForTest(m)
	assert.Equal(t, int64(150), offset)
	// An out-of-spec reference must degrade E3 even though phc2sys remains locked.
	m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, IFace: "eth0", Data: &event.OffsetData{State: event.PTP_FREERUN, Offset: 200}})
	msgs := osClockMessages(cache)
	require.Len(t, msgs, 1)
	assert.Equal(t, parserconstants.ClockRealTime, msgs[0].IFace)
	assert.Equal(t, event.PtpStateToIPCState(event.PTP_FREERUN), msgs[0].Values.(ipc.StateValue).State)
	assert.Equal(t, int64(150), msgs[0].Values.(ipc.StateValue).Offset)
	stats = receiveStatsSnapshot(t, m.StatsUpdatesCh())
	assert.Equal(t, event.PTP_LOCKED, stats.OSClockState, "E3 must not overwrite the disciplining state")
}

func TestOSClock_ReportsWithoutRegisteredClocksAndAfterReset(t *testing.T) {
	for _, source := range []event.EventSource{event.PHC2SYS, event.CHRONYD} {
		t.Run(string(source), func(t *testing.T) {
			cache := ipc.NewCache(20)
			m := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, cache)
			m.ConfigureOSClockFilter(clock.OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 1})
			ev := event.Event{Source: source, CfgName: "phc2sys.0.config", IFace: parserconstants.ClockRealTime, Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}}
			m.ProcessEvent(ev)
			msgs := osClockMessages(cache)
			require.Len(t, msgs, 1)
			assert.Equal(t, event.PtpStateToIPCState(event.PTP_LOCKED), msgs[0].Values.(ipc.StateValue).State)
			stats := receiveStatsSnapshot(t, m.StatsUpdatesCh())
			assert.Equal(t, event.PTP_LOCKED, stats.States[ev.CfgName][source])
			m.RemoveAllClocks()
			iface, _ := clockmgr.OSClockSampleForTest(m)
			assert.Equal(t, parserconstants.ClockRealTime, iface)
			stats = receiveStatsSnapshot(t, m.StatsUpdatesCh())
			assert.Equal(t, event.PTP_NOTSET, stats.OSClockE3State)
			m.ProcessEvent(ev)
			stats = receiveStatsSnapshot(t, m.StatsUpdatesCh())
			assert.Equal(t, event.PTP_LOCKED, stats.OSClockE3State, "a fresh sample after reset re-derives E3")
			// Cache deduplication may suppress the identical value, but its
			// snapshot must still contain the externally reported state.
			require.Len(t, cache.Snapshot(), 1)
			assert.Equal(t, event.PtpStateToIPCState(event.PTP_LOCKED), cache.Snapshot()[0].Values.(ipc.StateValue).State)
		})
	}
}

func TestStatsUpdates_HAMemberSourceIdentity(t *testing.T) {
	m := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	th := event.PtpClockThreshold{MinOffsetThreshold: -100, MaxOffsetThreshold: 100}
	first, err := clock.NewBC("ptp4l.0.config", false, th)
	require.NoError(t, err)
	second, err := clock.NewBC("ptp4l.1.config", false, th)
	require.NoError(t, err)
	ha, err := clock.NewHA("phc2sys.2.config", clock.HAMember{Profile: "first", Clock: first}, clock.HAMember{Profile: "second", Clock: second})
	require.NoError(t, err)
	require.NoError(t, m.AddClock(ha))
	for _, ev := range []event.Event{
		{Source: event.PTP4l, CfgName: first.ConfigName(), IFace: "eth0", Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}},
		{Source: event.PTP4l, CfgName: second.ConfigName(), IFace: "eth1", Data: &event.OffsetData{State: event.PTP_FREERUN, Offset: 200}},
	} {
		m.ProcessEvent(ev)
		m.ProcessEvent(ev) // The first sample creates interface details; the next fills the window.
	}
	stats := receiveStatsSnapshot(t, m.StatsUpdatesCh())
	assert.Equal(t, event.PTP_LOCKED, stats.States[first.ConfigName()][event.PTP4l])
	assert.Equal(t, event.PTP_FREERUN, stats.States[second.ConfigName()][event.PTP4l])
	firstWindow := stats.Windows[first.ConfigName()][event.PTP4l]
	secondWindow := stats.Windows[second.ConfigName()][event.PTP4l]
	require.NotNil(t, firstWindow)
	require.NotNil(t, secondWindow)
	assert.Equal(t, float64(10), firstWindow.LastInserted())
	assert.Equal(t, float64(200), secondWindow.LastInserted())
}

func TestStatsUpdates_WindowAvailabilityIsIndependentOfSource(t *testing.T) {
	m := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	bc, err := clock.NewBC(testClockmgrPtp4lConfig, true, event.PtpClockThreshold{})
	require.NoError(t, err)
	require.NoError(t, m.AddClock(bc))

	// A non-phc2sys source can expose state without an offset history too.
	windowed := bc.GetData(event.PTP4l)
	bc.GetData(event.TS2PHC).Window = nil
	m.ProcessEvent(event.Event{
		Source: event.TS2PHC, CfgName: "ts2phc.0.config", IFace: "eth0",
		Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10},
	})
	stats := receiveStatsSnapshot(t, m.StatsUpdatesCh())
	assert.Equal(t, event.PTP_LOCKED, stats.States[testClockmgrPtp4lConfig][event.TS2PHC])
	assert.Nil(t, stats.Windows[testClockmgrPtp4lConfig][event.TS2PHC])
	require.NotNil(t, windowed.Window)
	snapshot := stats.Windows[testClockmgrPtp4lConfig][event.PTP4l]
	require.NotNil(t, snapshot, "an allocated but empty window is still available")
	assert.NotSame(t, windowed.Window, snapshot, "snapshots must not alias live windows")
	assert.True(t, snapshot.IsEmpty())
}

func TestOSClock_UnchangedSamplesDoNotFallThrough(t *testing.T) {
	for _, source := range []event.EventSource{event.PHC2SYS, event.CHRONYD} {
		t.Run(string(source), func(t *testing.T) {
			m := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, nil)
			m.ConfigureOSClockFilter(clock.OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 2})
			bc, err := clock.NewBC(testClockmgrPtp4lConfig, true, event.PtpClockThreshold{MinOffsetThreshold: -100, MaxOffsetThreshold: 100})
			require.NoError(t, err)
			require.NoError(t, m.AddClock(bc))
			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, IFace: "eth0", Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}})
			require.Equal(t, event.PTP_LOCKED, bc.GetState())
			for _, offset := range []int64{1000, 2000, 3000} {
				m.ProcessEvent(event.Event{Source: source, CfgName: testClockmgrPtp4lConfig, IFace: parserconstants.ClockRealTime, Data: &event.OffsetData{State: event.PTP_FREERUN, Offset: offset}})
				_, latest := clockmgr.OSClockSampleForTest(m)
				assert.Equal(t, offset, latest)
				stats := receiveStatsSnapshot(t, m.StatsUpdatesCh())
				assert.Equal(t, event.PTP_FREERUN, stats.OSClockState)
				assert.Equal(t, event.PTP_LOCKED, bc.GetState(), "OS offsets must not reach the PTP clock, even without an OS state transition")
			}
		})
	}
}

func TestOSClock_HASelectionContinuesToHAClock(t *testing.T) {
	m := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	th := event.PtpClockThreshold{MinOffsetThreshold: -100, MaxOffsetThreshold: 100}
	first, err := clock.NewBC("ptp4l.0.config", false, th)
	require.NoError(t, err)
	second, err := clock.NewBC("ptp4l.1.config", false, th)
	require.NoError(t, err)
	ha, err := clock.NewHA("phc2sys.2.config", clock.HAMember{Profile: "first", Clock: first}, clock.HAMember{Profile: "second", Clock: second})
	require.NoError(t, err)
	require.NoError(t, m.AddClock(ha))
	for i, member := range []*clock.BCClock{first, second} {
		m.ProcessEvent(event.Event{Source: event.PMC, CfgName: member.ConfigName(), Data: &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: uint8(6 + i)}}})
		m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: member.ConfigName(), IFace: []string{"eth0", "eth1"}[i], Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}})
	}
	m.ProcessEvent(event.Event{Source: event.PHC2SYS, CfgName: ha.ConfigName(), Data: &event.SelectedSourceData{IFace: "eth1"}})
	assert.Equal(t, second.ClockClass(), ha.ClockClass(), "HA selection must reach the composite clock")
	iface, _ := clockmgr.OSClockSampleForTest(m)
	assert.Equal(t, parserconstants.ClockRealTime, iface, "selection is not an OS-clock sample")
	stats := receiveStatsSnapshot(t, m.StatsUpdatesCh())
	assert.Empty(t, stats.States[ha.ConfigName()])
}
