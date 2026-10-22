package clockmgr_test

import (
	"context"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func receiveStatsSnapshot(t *testing.T, updates <-chan process.EventStats) process.EventStats {
	t.Helper()
	select {
	case stats := <-updates:
		return stats
	case <-time.After(time.Second):
		t.Fatal("clock-data snapshot was not pushed")
		return process.EventStats{}
	}
}

func TestStatsUpdatesPushUnchangedStates(t *testing.T) {
	m := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	updates := m.StatsUpdatesCh()
	assert.Equal(t, updates, m.StatsUpdatesCh(), "there is one manager channel, not a subscription per reader")
	initial := receiveStatsSnapshot(t, updates)
	assert.Equal(t, event.PTP_FREERUN, initial.OSClockState)
	assert.Empty(t, initial.States)
	for i := int64(1); i <= 3; i++ {
		m.ProcessEvent(event.Event{Source: event.PHC2SYS, CfgName: "phc2sys.0.config", IFace: "CLOCK_REALTIME", Data: &event.OffsetData{State: event.PTP_FREERUN, Offset: i}})
		stats := receiveStatsSnapshot(t, updates)
		assert.Equal(t, event.PTP_FREERUN, stats.States["phc2sys.0.config"][event.PHC2SYS])
		assert.Equal(t, event.PTP_FREERUN, stats.OSClockState)
		assert.Nil(t, stats.Windows["phc2sys.0.config"][event.PHC2SYS])
	}
	assert.Empty(t, m.SyncStatusUpdateCh(), "unchanged samples do not fabricate sync-state transitions")
	m.ProcessEvent(event.Event{Source: event.PHC2SYS, CfgName: "phc2sys.0.config", IFace: "CLOCK_REALTIME", Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}})
	assert.Equal(t, event.PTP_LOCKED, receiveStatsSnapshot(t, updates).OSClockState)
	assert.Len(t, m.SyncStatusUpdateCh(), 1, "the separate sync-status channel still reports transitions")
}

func TestStatsUpdatesRetainLatestForSlowOrLateManager(t *testing.T) {
	m := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	bc, err := clock.NewBC(testClockmgrPtp4lConfig, true, event.PtpClockThreshold{MaxOffsetThreshold: 100, MinOffsetThreshold: -100})
	require.NoError(t, err)
	require.NoError(t, m.AddClock(bc))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for offset := int64(1); offset <= 20; offset++ {
			m.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, IFace: "eth0", Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: offset}})
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("clock processing blocked while the manager was not reading")
	}
	updates := m.StatsUpdatesCh()
	assert.Len(t, updates, 1, "only the latest complete snapshot is retained")
	latest := receiveStatsSnapshot(t, updates)
	assert.Equal(t, event.PTP_LOCKED, latest.ClockStates[testClockmgrPtp4lConfig])
	assert.Equal(t, float64(20), latest.Windows[testClockmgrPtp4lConfig][event.PTP4l].LastInserted())
	assert.Empty(t, updates, "the manager must not receive an obsolete backlog")
}

func TestStatsUpdatesPushProcessedWindowSnapshots(t *testing.T) {
	events := make(chan event.Event, 2)
	m := clockmgr.Init("test-node", events, nil, nil, nil, nil)
	bc, err := clock.NewBC(testClockmgrPtp4lConfig, true, event.PtpClockThreshold{MaxOffsetThreshold: 100, MinOffsetThreshold: -100})
	require.NoError(t, err)
	require.NoError(t, m.AddClock(bc))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := m.StatsUpdatesCh()
	receiveStatsSnapshot(t, updates) // Latest registry snapshot before measurements.
	go m.ProcessEvents(ctx)
	var before process.EventStats
	for i := 0; i < 3; i++ {
		events <- event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, IFace: "eth0", Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}}
		before = receiveStatsSnapshot(t, updates)
		assert.Equal(t, event.PTP_LOCKED, before.States[testClockmgrPtp4lConfig][event.PTP4l])
		assert.Equal(t, i, before.Windows[testClockmgrPtp4lConfig][event.PTP4l].CountSamples(func(float64) bool { return true }),
			"each pushed snapshot follows insertion, even when the source stays LOCKED")
	}
	events <- event.Event{Source: event.PTP4l, CfgName: testClockmgrPtp4lConfig, IFace: "eth0", Data: &event.OffsetData{State: event.PTP_FREERUN, Offset: 200}}
	after := receiveStatsSnapshot(t, updates)
	assert.Equal(t, event.PTP_FREERUN, after.States[testClockmgrPtp4lConfig][event.PTP4l])
	assert.Equal(t, event.PTP_LOCKED, before.States[testClockmgrPtp4lConfig][event.PTP4l])
	assert.Equal(t, float64(10), before.Windows[testClockmgrPtp4lConfig][event.PTP4l].LastInserted(), "later processing cannot mutate a pushed snapshot")
}

func TestStatsUpdatesPushInvalidations(t *testing.T) {
	m := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	bc, err := clock.NewBC(testClockmgrPtp4lConfig, true, event.PtpClockThreshold{})
	require.NoError(t, err)
	require.NoError(t, m.AddClock(bc))
	updates := m.StatsUpdatesCh()
	assert.Contains(t, receiveStatsSnapshot(t, updates).ClockStates, testClockmgrPtp4lConfig)
	m.ProcessEvent(event.Event{Source: event.CHRONYD, CfgName: "chronyd.0.config", Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}})
	assert.Equal(t, event.PTP_LOCKED, receiveStatsSnapshot(t, updates).States["chronyd.0.config"][event.CHRONYD], "OS-only configs must be pushed without a registered PTP clock")
	m.RemoveAllClocks()
	cleared := receiveStatsSnapshot(t, updates)
	assert.Empty(t, cleared.States)
	assert.Empty(t, cleared.Windows)
	assert.Empty(t, cleared.ClockStates)
	assert.Equal(t, event.PTP_FREERUN, cleared.OSClockState)
	m.ProcessEvent(event.Event{Reset: true})
	assert.Equal(t, event.PTP_NOTSET, receiveStatsSnapshot(t, updates).OSClockE3State)
}
