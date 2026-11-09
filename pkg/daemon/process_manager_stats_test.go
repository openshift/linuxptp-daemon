package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type observedProcessStateCondition struct {
	process.OnProcessState
	observed chan event.PTPState
}

func (c observedProcessStateCondition) MetByCurrentState(p process.Process, sourceUp process.SourceStateQuery, stats process.EventStats) bool {
	state := stats.States[c.ConfigName][event.EventSource(c.Source)]
	select {
	case c.observed <- state:
	default:
	}
	return c.OnProcessState.MetByCurrentState(p, sourceUp, stats)
}

func waitForObservedState(t *testing.T, states <-chan event.PTPState, expected event.PTPState) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case state := <-states:
			if state == expected {
				return
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for evaluated state %s", expected)
		}
	}
}

func TestStatsUpdatesWaitForClockConsumer(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC, ptp4lConfig)
	ct.cm.ConfigureOSClockFilter(clock.OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 1})
	waiter := ct.AddProcess(&stubProcess{
		name: "waiter", state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessState{ConfigName: ptp4lConfig, Source: string(event.PHC2SYS), State: event.PTP_LOCKED},
		},
	})
	ct.StartPM() // Deliberately leave the clock consumer stopped.
	ct.SendOffset(event.PHC2SYS, ptp4lConfig, event.PTP_LOCKED, 10)
	require.Eventually(t, func() bool { return len(ct.pm.eventsOut) == 1 }, time.Second, time.Millisecond,
		"raw data must go through the clock-manager channel")
	assert.Zero(t, waiter.Starts(), "forwarding a sample is not the same as evaluating it")

	ct.StartCM()
	require.Eventually(t, func() bool { return waiter.Starts() == 1 }, time.Second, time.Millisecond,
		"the pushed snapshot must start the waiter without another input event or a periodic tick")
}

func TestStatsUpdatesReconcileAlreadyReadyClock(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC, ptp4lConfig)
	ct.cm.ConfigureOSClockFilter(clock.OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 1})
	ct.cm.ProcessEvent(event.Event{Source: event.PHC2SYS, CfgName: ptp4lConfig, IFace: clockRealTime, Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}})
	waiter := ct.AddProcess(&stubProcess{
		name: "late-waiter", state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessState{ConfigName: ptp4lConfig, Source: string(event.PHC2SYS), State: event.PTP_LOCKED},
		},
	})
	ct.StartPM()
	require.Eventually(t, func() bool { return waiter.Starts() == 1 }, time.Second, time.Millisecond,
		"the retained snapshot must recover already-satisfied prerequisites without a query")
}

func TestSourceQualificationStartupAndRestart(t *testing.T) {
	ct := NewCondiitonsTester(t, event.TBC, ptp4lConfig)
	ct.cm.ConfigureOSClockFilter(clock.OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 1})
	generation, err := ct.cm.ConfigureSourceQualification(ptp4lConfig, []string{"tr0"}, utils.QualificationConfig{
		QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 1, DisqualifiedSamples: 1})
	require.NoError(t, err)
	setQualified := func(qualified bool) {
		if qualified {
			ct.cm.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: ptp4lConfig, IFace: "tr0", Data: &event.PTPSourceObservation{
				Generation: generation, PortRole: utils.ToPtr(parserconstants.PortRoleSlave)}})
		}
		ct.cm.ProcessEvent(event.Event{Source: event.PTP4l, CfgName: ptp4lConfig, IFace: "tr0", Data: &event.PTPSourceObservation{
			Generation: generation, ServoState: "s2", Offset: 10, SourceLost: !qualified}})
	}
	setQualified(true)
	setQualified(false) // no historical TRUE may release the pending process
	waiter := ct.AddProcess(&stubProcess{
		name: ts2phcProcessName, cfgName: "ts2phc.0.config", state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: &process.All{Conditions: []process.Condition{
				process.OnSourceQualified{ConfigName: ptp4lConfig},
				process.OnProcessState{ConfigName: ptp4lConfig, Source: string(event.PHC2SYS), State: event.PTP_LOCKED},
			}},
		},
	})
	ct.cm.ProcessEvent(event.Event{Source: event.PHC2SYS, CfgName: ptp4lConfig, IFace: clockRealTime, Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10}})
	ct.pm.reconcileWaiting(context.Background())
	require.Zero(t, waiter.Starts())
	setQualified(true)
	ct.pm.reconcileWaiting(context.Background())
	require.Equal(t, 1, waiter.Starts())
	setQualified(false)
	ct.pm.reconcileWaiting(context.Background())
	require.Zero(t, waiter.Stops(), "qualification is not a downstream kill policy")
	// Existing process-down recovery deliberately bypasses initial gates.
	waiter.mu.Lock()
	waiter.state = process.Dead
	waiter.mu.Unlock()
	ct.pm.handleProcessDown(context.Background(), event.ProcessStatusEvent(event.TS2PHC, "ts2phc.0.config", event.TBC, "", PtpProcessDown))
	require.Equal(t, 2, waiter.Starts())
}

func TestStatsUpdatesDoNotLatchDegradedReadiness(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC, ptp4lConfig)
	ct.cm.ConfigureOSClockFilter(clock.OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 1})
	observedStates := make(chan event.PTPState, 10)
	waiter := ct.AddProcess(&stubProcess{
		name: "mixed-waiter", state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: &process.All{Conditions: []process.Condition{
				observedProcessStateCondition{
					OnProcessState: process.OnProcessState{ConfigName: ptp4lConfig, Source: string(event.PHC2SYS), State: event.PTP_LOCKED},
					observed:       observedStates,
				},
				process.OnPluginEvent{EventName: "ready"},
			}},
		},
	})
	ct.Start()
	ct.SendOffset(event.PHC2SYS, ptp4lConfig, event.PTP_LOCKED, 10)
	waitForObservedState(t, observedStates, event.PTP_LOCKED)
	ct.SendOffset(event.PHC2SYS, ptp4lConfig, event.PTP_FREERUN, 200)
	waitForObservedState(t, observedStates, event.PTP_FREERUN)
	ct.SendPluginEvent("plugin", "ready")
	assert.Zero(t, waiter.Starts(), "a previously ready clock cannot satisfy the conjunction after degradation")
	ct.SendOffset(event.PHC2SYS, ptp4lConfig, event.PTP_LOCKED, 10)
	waitForObservedState(t, observedStates, event.PTP_LOCKED)
	require.Eventually(t, func() bool { return waiter.Starts() == 1 }, time.Second, time.Millisecond)
}

func TestStatsUpdatesCanStopOnEvaluatedClockState(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC, ptp4lConfig)
	ct.cm.ConfigureOSClockFilter(clock.OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 1})
	running := ct.AddProcess(&stubProcess{
		name: "running", state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStop: process.OnProcessState{ConfigName: ptp4lConfig, Source: string(event.PHC2SYS), State: event.PTP_FREERUN},
		},
	})
	ct.Start()
	ct.SendOffset(event.PHC2SYS, ptp4lConfig, event.PTP_LOCKED, 10)
	assert.Zero(t, running.Stops())
	ct.SendOffset(event.PHC2SYS, ptp4lConfig, event.PTP_FREERUN, 200)
	require.Eventually(t, func() bool { return running.Stops() == 1 }, time.Second, time.Millisecond)
}

func TestStatsUpdatesDoNotReplayPluginActions(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC, ptp4lConfig)
	p := ct.AddProcess(&stubProcess{
		name: "plugin-waiter", state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: "ready"},
			process.ActionStop:  process.OnPluginEvent{EventName: "ready"},
		},
	})
	ct.Start()
	ct.SendPluginEvent("plugin", "ready")
	require.Eventually(t, func() bool { return p.Starts() == 1 }, time.Second, time.Millisecond)
	ct.SendOffset(event.PHC2SYS, ptp4lConfig, event.PTP_LOCKED, 10)
	assert.Zero(t, p.Stops(), "a pushed snapshot cannot replay the earlier plugin event as a stop action")
	ct.SendPluginEvent("plugin", "ready")
	require.Eventually(t, func() bool { return p.Stops() == 1 }, time.Second, time.Millisecond)
}

func TestStatsUpdatesUsePushedStatesAndWindowsWithoutClockManager(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan process.EventStats, 1)
	waiter := &stubProcess{
		name: "snapshot-waiter", state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: &process.All{Conditions: []process.Condition{
				process.OnProcessState{ConfigName: ptp4lConfig, Source: string(event.PHC2SYS), State: event.PTP_LOCKED},
				process.OnStateAndOffsetForCount{ClockID: ptp4lConfig, ConfigName: ptp4lConfig, Source: event.PTP4l, State: event.PTP_LOCKED, MaxOffset: 100, Count: 1},
			}},
		},
	}
	pm := &ProcessManager{process: []process.Process{waiter}, eventsIn: make(chan event.Event), statsIn: updates}
	go pm.processEvents(ctx)
	w := utils.NewWindow(3)
	w.Insert(10)
	w.Insert(20)
	updates <- process.EventStats{
		States:  map[string]map[event.EventSource]event.PTPState{ptp4lConfig: {event.PTP4l: event.PTP_LOCKED, event.PHC2SYS: event.PTP_LOCKED}},
		Windows: map[string]map[event.EventSource]utils.ROWindow{ptp4lConfig: {event.PTP4l: w.Snapshot()}},
	}
	require.Eventually(t, func() bool { return waiter.Starts() == 1 }, time.Second, time.Millisecond,
		"the process manager must use the delivered snapshot, without a ClockManager or a stats query")
}

func TestStatsUpdatesUseNewestQueuedStateForPluginReconciliation(t *testing.T) {
	ready := process.EventStats{States: map[string]map[event.EventSource]event.PTPState{ptp4lConfig: {event.PHC2SYS: event.PTP_LOCKED}}}
	degraded := process.EventStats{States: map[string]map[event.EventSource]event.PTPState{ptp4lConfig: {event.PHC2SYS: event.PTP_FREERUN}}}
	updates := make(chan process.EventStats, 1)
	waiter := &stubProcess{
		name: "mixed-snapshot-waiter", state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: &process.All{Conditions: []process.Condition{
				process.OnProcessState{ConfigName: ptp4lConfig, Source: string(event.PHC2SYS), State: event.PTP_LOCKED},
				process.OnPluginEvent{EventName: "ready"},
			}},
		},
	}
	pm := &ProcessManager{process: []process.Process{waiter}, statsIn: updates, latestStats: ready}
	updates <- degraded
	pm.evalActions(context.Background(), event.PluginEvent("plugin", "ready"))
	pm.reconcileWaiting(context.Background())
	assert.Zero(t, waiter.Starts(), "a newer pushed degradation must override the previously cached LOCKED state")
	updates <- ready
	pm.reconcileWaiting(context.Background())
	assert.Equal(t, 1, waiter.Starts())
}

func TestStatsUpdatesClearCachedReadiness(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cleared snapshot", true: "closed input"}[closed], func(t *testing.T) {
			updates := make(chan process.EventStats, 1)
			waiter := &stubProcess{
				name: "invalidated-waiter", state: process.Created,
				conds: map[process.Action]process.Condition{
					process.ActionStart: process.OnProcessState{ConfigName: ptp4lConfig, Source: string(event.PHC2SYS), State: event.PTP_LOCKED},
				},
			}
			pm := &ProcessManager{
				process: []process.Process{waiter}, statsIn: updates,
				latestStats: process.EventStats{States: map[string]map[event.EventSource]event.PTPState{ptp4lConfig: {event.PHC2SYS: event.PTP_LOCKED}}},
			}
			if closed {
				close(updates)
			} else {
				updates <- process.EventStats{}
			}
			pm.reconcileWaiting(context.Background())
			assert.Zero(t, waiter.Starts(), "invalidated clock data must not survive in the cached snapshot")
			assert.Empty(t, pm.latestStats.States)
			if closed {
				assert.Nil(t, pm.statsIn)
			}
		})
	}
}
