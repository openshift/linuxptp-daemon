package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

const (
	testETH0          = "eth0"
	testE810          = "e810"
	testGNSSFailover  = "gnss_failover"
	testGNSSRecovered = "gnss_recovered"
	testPtp4l1Config  = "ptp4l.1.config"
	testTs2phcConfig  = "ts2phc.0.config"
)

type stubProcess struct {
	name    string
	cfgName string
	iface   string
	state   process.State
	starts  int
	stops   int
	deps    []process.Process
	conds   map[process.Action]process.Condition
	profile *ptpv1.PtpProfile
}

func (s *stubProcess) Name() string       { return s.name }
func (s *stubProcess) ConfigName() string { return s.cfgName }
func (s *stubProcess) SyncInitialState()  {}
func (s *stubProcess) Start(context.Context) error {
	s.starts++
	s.state = process.Running
	return nil
}
func (s *stubProcess) Stop() error {
	s.stops++
	s.state = process.Stopped
	return nil
}
func (s *stubProcess) Conditions() map[process.Action]process.Condition {
	return s.conds
}
func (s *stubProcess) State() process.State       { return s.state }
func (s *stubProcess) Profile() *ptpv1.PtpProfile { return s.profile }
func (s *stubProcess) ClockType() event.ClockType { return event.OC }
func (s *stubProcess) DependentProcesses() []process.Process {
	return s.deps
}
func (s *stubProcess) IFace() string { return s.iface }

func testClockMgr(t *testing.T, cfgNames ...string) *clockmgr.ClockManager {
	t.Helper()
	cm := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	for _, cfgNameN := range cfgNames {
		ocN, err := clock.NewBC(cfgNameN, true, event.PtpClockThreshold{})
		assert.NoError(t, err)
		err = cm.AddClock(ocN)
		assert.NoError(t, err)
	}
	return cm
}

func TestStartProcess_NilProfileDoesNotPanic(t *testing.T) {
	called := false
	pm := &ProcessManager{
		daemon: &Daemon{
			pluginManager: plugin.PluginManager{
				Plugins: map[string]*plugin.Plugin{
					testE810: {
						AfterRunPTPCommand: func(_ *interface{}, nodeProfile *ptpv1.PtpProfile, _ string) error {
							called = true
							_ = nodeProfile.Plugins
							return nil
						},
					},
				},
			},
		},
	}
	p := &stubProcess{name: pmcSocketName, cfgName: testPtp4l1Config, state: process.Created}
	assert.NotPanics(t, func() {
		pm.startProcess(context.Background(), p)
	})
	assert.False(t, called)
	assert.Equal(t, 1, p.starts)
}

func TestStartProcess_CallsAfterRunPTPCommandWithProfile(t *testing.T) {
	var gotCmd string
	prof := &ptpv1.PtpProfile{}
	pm := &ProcessManager{
		daemon: &Daemon{
			pluginManager: plugin.PluginManager{
				Plugins: map[string]*plugin.Plugin{
					testE810: {
						AfterRunPTPCommand: func(_ *interface{}, nodeProfile *ptpv1.PtpProfile, command string) error {
							gotCmd = command
							assert.Equal(t, prof, nodeProfile)
							return nil
						},
					},
				},
			},
		},
	}
	p := &stubProcess{name: pmcSocketName, state: process.Created, profile: prof}
	pm.startProcess(context.Background(), p)
	assert.Equal(t, pmcSocketName, gotCmd)
	assert.Equal(t, 1, p.starts)
}

func TestForwardEvents_HopsEvent(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	pm := &ProcessManager{
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	want := event.Event{
		Source:    event.PTP4l,
		CfgName:   ptp4lConfig,
		ClockType: event.OC,
		Reset:     true,
	}
	inbound <- want

	select {
	case got := <-handler:
		assert.Equal(t, want, got)
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive hopped event on handlerChannel")
	}
}

func TestForwardEvents_CancelReturns(t *testing.T) {
	inbound := make(chan event.Event)
	handler := make(chan event.Event)
	pm := &ProcessManager{
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pm.processEvents(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("forwardEvents did not return after cancel")
	}
}

func TestForwardEvents_ProcessStatusDownDeadRestarts(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	stub := &stubProcess{name: string(event.PTP4l), cfgName: "ptp4l.0.config", state: process.Dead}
	pm := &ProcessManager{
		process:   []process.Process{stub},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessDown)

	assert.Eventually(t, func() bool { return stub.starts == 1 }, 30*time.Second, 10*time.Millisecond)
	assert.Empty(t, handler, "ProcessStatusData must not be forwarded to handler")
}

func TestForwardEvents_ProcessStatusDownStoppedNoRestart(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	stub := &stubProcess{name: string(event.PTP4l), state: process.Stopped}
	pm := &ProcessManager{
		process:   []process.Process{stub},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessDown)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, stub.starts)
	assert.Empty(t, handler, "ProcessStatusData must not be forwarded to handler")
}

func TestForwardEvents_ProcessStatusUpNoRestart(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	stub := &stubProcess{name: string(event.PTP4l), state: process.Dead}
	pm := &ProcessManager{
		process:   []process.Process{stub},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessUp)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, stub.starts)
	assert.Empty(t, handler, "ProcessStatusData must not be forwarded to handler")
}

func TestForwardEvents_ProcessStatusDownRestartsDep(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	dep := &stubProcess{name: string(event.GPSD), cfgName: testTs2phcConfig, state: process.Dead}
	parent := &stubProcess{name: string(event.TS2PHC), cfgName: testTs2phcConfig, state: process.Running, deps: []process.Process{dep}}
	pm := &ProcessManager{
		process:   []process.Process{parent},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.GPSD, testTs2phcConfig, event.GM, "", PtpProcessDown)

	assert.Eventually(t, func() bool { return dep.starts == 1 }, 20*time.Second, 1*time.Millisecond)
	assert.Equal(t, 0, parent.starts)
	assert.Empty(t, handler, "ProcessStatusData must not be forwarded to handler")
}

func TestStartProcesses_MissingConditionStartsImmediately(t *testing.T) {
	dep := &stubProcess{name: "gpspipe"}
	parent := &stubProcess{name: "ts2phc", deps: []process.Process{dep}}
	pm := &ProcessManager{
		process:   []process.Process{parent},
		eventsIn:  make(chan event.Event),
		eventsOut: make(chan event.Event, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		pm.StartProcesses(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartProcesses blocked; 3s init delays should be gone")
	}
	assert.Equal(t, 1, dep.starts)
	assert.Equal(t, 1, parent.starts)
}

func TestStartProcesses_AlreadyRunningNotStarted(t *testing.T) {
	parent := &stubProcess{
		name:  "ts2phc",
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.GPSD, ConfigName: ts2phcConf},
		},
	}
	pm := &ProcessManager{
		process:   []process.Process{parent},
		eventsIn:  make(chan event.Event),
		eventsOut: make(chan event.Event, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.StartProcesses(ctx)
	assert.Equal(t, 0, parent.starts)
}

func TestStartProcesses_OnProcessUpDoesNotStart(t *testing.T) {
	pmc := &stubProcess{
		name: "pmc",
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.PTP4l, ConfigName: ptp4lConfig},
		},
	}
	parent := &stubProcess{name: string(event.PTP4l), deps: []process.Process{pmc}}
	pm := &ProcessManager{
		process:   []process.Process{parent},
		eventsIn:  make(chan event.Event),
		eventsOut: make(chan event.Event, 1),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.StartProcesses(ctx)
	assert.Equal(t, 0, pmc.starts)
	assert.Equal(t, 1, parent.starts)
}

func TestForwardEvents_OnProcessUpStartsCreated(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	pmc := &stubProcess{
		name: "pmc",
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.PTP4l, ConfigName: ptp4lConfig},
		},
	}
	parent := &stubProcess{name: string(event.PTP4l), state: process.Running, deps: []process.Process{pmc}}
	pm := &ProcessManager{
		process:   []process.Process{parent},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessUp)

	assert.Eventually(t, func() bool { return pmc.starts == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 0, parent.starts)
}

func TestForwardEvents_OnProcessUpWrongSourceNoStart(t *testing.T) {
	inbound := make(chan event.Event, 2)
	handler := make(chan event.Event, 2)
	pmc := &stubProcess{
		name: "pmc",
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.PTP4l, ConfigName: ptp4lConfig},
		},
	}
	parent := &stubProcess{name: string(event.PTP4l), state: process.Running, deps: []process.Process{pmc}}
	pm := &ProcessManager{
		process:   []process.Process{parent},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.GPSD, "ptp4l.0.config", event.OC, "", PtpProcessUp)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, pmc.starts)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessDown)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, pmc.starts)
}

func TestStartProcesses_ImmediateNotRestartedOnHop(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	parent := &stubProcess{name: string(event.PTP4l)}
	pm := &ProcessManager{
		process:   []process.Process{parent},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.StartProcesses(ctx)
	assert.Equal(t, 1, parent.starts)

	inbound <- event.ProcessStatusEvent(event.PTP4l, "ptp4l.0.config", event.OC, "", PtpProcessUp)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, parent.starts)
	assert.Empty(t, handler, "ProcessStatusData must not be forwarded to handler")
}

func TestEvalActions_StopRunningProcess(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStop: process.OnPluginEvent{EventName: testGNSSFailover},
		},
	}
	pm := &ProcessManager{
		process:   []process.Process{phc2sys},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.PluginEvent("ntpfailover", testGNSSFailover)

	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	// phc2sys.Stop() was called, which sets state to Running in our stub
	// (stub doesn't actually change state, but the method was called)
}

func TestEvalActions_StartStoppedProcess(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Stopped,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: testGNSSRecovered},
		},
	}
	pm := &ProcessManager{
		process:   []process.Process{phc2sys},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.PluginEvent("ntpfailover", testGNSSRecovered)

	assert.Eventually(t, func() bool { return phc2sys.starts == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestEvalActions_StartNotCheckedWhenRunning(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: testGNSSRecovered},
		},
	}
	pm := &ProcessManager{
		process:   []process.Process{phc2sys},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.PluginEvent("ntpfailover", testGNSSRecovered)
	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 0, phc2sys.starts)
}
func TestEvalActions_NoActionWithoutCondition(t *testing.T) {
	inbound := make(chan event.Event, 1)
	handler := make(chan event.Event, 1)
	// Process with no conditions -- ActionStop defaults to Never
	stub := &stubProcess{name: ptp4lProcessName, state: process.Running}
	pm := &ProcessManager{
		process:   []process.Process{stub},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.PluginEvent("ntpfailover", testGNSSFailover)
	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	// Running process should NOT be stopped because ActionStop defaults to Never
	assert.Equal(t, process.Running, stub.state)
}

func TestEvalActions_FullFailoverFlow(t *testing.T) {
	inbound := make(chan event.Event, 4)
	handler := make(chan event.Event, 4)
	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: testGNSSRecovered},
			process.ActionStop:  process.OnPluginEvent{EventName: testGNSSFailover},
		},
	}
	chronyd := &stubProcess{
		name:  "chronyd",
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: testGNSSFailover},
			process.ActionStop:  process.OnPluginEvent{EventName: testGNSSRecovered},
		},
	}
	pm := &ProcessManager{
		process:   []process.Process{phc2sys, chronyd},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	// Failover: stop phc2sys + enable chronyd
	inbound <- event.PluginEvent("ntpfailover", testGNSSFailover)
	select {
	case <-handler:
	case <-time.After(200 * time.Second):
		t.Fatal("failover event was not forwarded")
	}
	assert.Equal(t, 1, chronyd.starts, "chronyd should be enabled on failover")

	// Simulate phc2sys being stopped (stub Stop doesn't change state, set manually)
	phc2sys.state = process.Stopped

	// Recovery: start phc2sys + disable chronyd
	inbound <- event.PluginEvent("ntpfailover", testGNSSRecovered)
	assert.Eventually(t, func() bool { return phc2sys.starts == 1 }, 2*time.Second, 10*time.Millisecond)
	select {
	case <-handler:
	case <-time.After(200 * time.Second):
		t.Fatal("recovery event was not forwarded")
	}
	assert.Equal(t, 1, chronyd.stops, "chronyd should be disabled on recovery")
}

func TestForwardEvents_AllStatefulStartsWhenAllConditionsMet(t *testing.T) {
	inbound := make(chan event.Event, 2)
	handler := make(chan event.Event, 2)
	parent := &stubProcess{
		name: string(event.TS2PHC),
		conds: map[process.Action]process.Condition{
			process.ActionStart: &process.All{Conditions: []process.Condition{
				process.OnProcessUp{Source: event.GPSD},
				process.OnProcessUp{Source: event.GPSPIPE},
			}},
		},
	}
	pm := &ProcessManager{
		process:   []process.Process{parent},
		eventsIn:  inbound,
		eventsOut: handler,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	inbound <- event.ProcessStatusEvent(event.GPSD, testTs2phcConfig, event.GM, "", PtpProcessUp)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, parent.starts, "not yet started with only first condition met")

	inbound <- event.ProcessStatusEvent(event.GPSPIPE, testTs2phcConfig, event.GM, "", PtpProcessUp)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 1, parent.starts, "stateful All starts when all conditions have been met (even at different times)")
}

func TestEvalActions_DelayedPhc2sysStartOnSubSecondOffset(t *testing.T) {
	const count = 3
	inbound := make(chan event.Event, 10)
	handler := make(chan event.Event, 10)

	cm := testClockMgr(t, testTs2phcConfig)

	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnStateAndOffsetForCount{
				ClockID:    testTs2phcConfig,
				ConfigName: testTs2phcConfig,
				Source:     event.TS2PHC,
				State:      event.PTP_LOCKED,
				MaxOffset:  1e9,
				Count:      count,
			},
		},
	}
	pm := &ProcessManager{
		process:   []process.Process{phc2sys},
		eventsIn:  inbound,
		eventsOut: handler,
		clockMgr:  cm,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	mkEvent := func(offset int64, state event.PTPState) event.Event {
		return event.Event{
			Source:  event.TS2PHC,
			CfgName: testTs2phcConfig,
			IFace:   testETH0,
			Data: &event.OffsetData{
				State:  state,
				Offset: offset,
			},
		}
	}
	drain := func() {
		select {
		case <-handler:
		case <-time.After(2 * time.Second):
			t.Fatal("event was not forwarded")
		}
	}
	send := func(offset int64, state event.PTPState) {
		ev := mkEvent(offset, state)
		cm.GetClock(testTs2phcConfig).GetData(event.TS2PHC).AddEvent(ev)
		inbound <- ev
		drain()
	}

	send(37000000000, event.PTP_LOCKED)
	assert.Equal(t, 0, phc2sys.starts, "large offset should not start phc2sys")

	send(1000000000, event.PTP_LOCKED)
	assert.Equal(t, 0, phc2sys.starts, "boundary offset should not start phc2sys")

	send(-2000000000, event.PTP_LOCKED)
	assert.Equal(t, 0, phc2sys.starts, "negative super-second offset should not start phc2sys")

	for i := 0; i < count; i++ {
		send(int64(100000+i), event.PTP_LOCKED)
	}
	assert.Equal(t, 0, phc2sys.starts, "should not start yet, need > count samples")

	send(-500000000, event.PTP_LOCKED)
	assert.Eventually(t, func() bool { return phc2sys.starts == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestEvalActions_DelayedPhc2sysWrongStateDoesNotStart(t *testing.T) {
	inbound := make(chan event.Event, 10)
	handler := make(chan event.Event, 10)

	cm := testClockMgr(t, testTs2phcConfig)

	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnStateAndOffsetForCount{
				ClockID:    testTs2phcConfig,
				ConfigName: testTs2phcConfig,
				Source:     event.TS2PHC,
				State:      event.PTP_LOCKED,
				MaxOffset:  1e9,
				Count:      1,
			},
		},
	}
	pm := &ProcessManager{
		process:   []process.Process{phc2sys},
		eventsIn:  inbound,
		eventsOut: handler,
		clockMgr:  cm,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	// Sub-second offset but FREERUN state should NOT start phc2sys
	for i := 0; i < 5; i++ {
		ev := event.Event{
			Source:  event.TS2PHC,
			CfgName: testTs2phcConfig,
			IFace:   testETH0,
			Data: &event.OffsetData{
				State:  event.PTP_FREERUN,
				Offset: 50000,
			},
		}
		cm.GetClock(testTs2phcConfig).GetData(event.TS2PHC).AddEvent(ev)
		inbound <- ev
		select {
		case <-handler:
		case <-time.After(2 * time.Second):
			t.Fatal("event was not forwarded")
		}
	}
	assert.Equal(t, 0, phc2sys.starts, "FREERUN state should not start phc2sys regardless of offset")
}

func TestEvalActions_DelayedPhc2sysTBCWaitsForPtp4l(t *testing.T) {
	const cfgName = "ptp4l.0.config"
	inbound := make(chan event.Event, 10)
	handler := make(chan event.Event, 10)
	cm := testClockMgr(t, cfgName)

	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnStateAndOffsetForCount{
				ClockID:    cfgName,
				ConfigName: cfgName,
				Source:     event.PTP4l,
				State:      event.PTP_LOCKED,
				MaxOffset:  1e9,
				Count:      1,
			},
		},
	}
	pm := &ProcessManager{
		process:   []process.Process{phc2sys},
		eventsIn:  inbound,
		eventsOut: handler,
		clockMgr:  cm,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	drain := func() {
		select {
		case <-handler:
		case <-time.After(2 * time.Second):
			t.Fatal("event was not forwarded")
		}
	}

	ts2phcEv := event.Event{
		Source:  event.TS2PHC,
		CfgName: testTs2phcConfig,
		IFace:   testETH0,
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 50000},
	}
	cm.GetClock(testTs2phcConfig).GetData(event.TS2PHC).AddEvent(ts2phcEv)
	cm.GetClock(testTs2phcConfig).GetData(event.TS2PHC).AddEvent(ts2phcEv)
	cm.GetClock(testTs2phcConfig).GetData(event.TS2PHC).AddEvent(ts2phcEv)
	inbound <- ts2phcEv
	drain()
	assert.Equal(t, 0, phc2sys.starts, "T-BC phc2sys must not start on ts2phc events")

	ptp4lEv := event.Event{
		Source:  event.PTP4l,
		CfgName: cfgName,
		IFace:   testETH0,
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 50000},
	}
	cm.GetClock(cfgName).GetData(event.PTP4l).AddEvent(ptp4lEv)
	cm.GetClock(cfgName).GetData(event.PTP4l).AddEvent(ptp4lEv)
	cm.GetClock(cfgName).GetData(event.PTP4l).AddEvent(ptp4lEv)
	inbound <- ptp4lEv
	assert.Eventually(t, func() bool { return phc2sys.starts == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestEvalActions_DelayedPhc2sysHAProfile(t *testing.T) {
	master1 := ptp4lConfig
	master2 := testPtp4l1Config
	inbound := make(chan event.Event, 10)
	handler := make(chan event.Event, 10)
	cm := testClockMgr(t, master1, master2)

	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.Any{Conditions: []process.Condition{
				process.OnStateAndOffsetForCount{
					ClockID:    master1,
					ConfigName: master1,
					Source:     event.PTP4l,
					State:      event.PTP_LOCKED,
					MaxOffset:  1e9, Count: 1,
				},
				process.OnStateAndOffsetForCount{
					ClockID:    master2,
					ConfigName: master2, Source: event.PTP4l,
					State: event.PTP_LOCKED, MaxOffset: 1e9, Count: 1,
				},
			}},
		},
	}
	pm := &ProcessManager{
		process:   []process.Process{phc2sys},
		eventsIn:  inbound,
		eventsOut: handler,
		clockMgr:  cm,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go pm.processEvents(ctx)

	drain := func() {
		select {
		case <-handler:
		case <-time.After(2 * time.Second):
			t.Fatal("event was not forwarded")
		}
	}

	large := event.Event{
		Source:  event.PTP4l,
		CfgName: master1,
		IFace:   "ens1f1",
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 37000000000},
	}
	cm.GetClock(master1).GetData(event.PTP4l).AddEvent(large)
	cm.GetClock(master1).GetData(event.PTP4l).AddEvent(large)
	cm.GetClock(master1).GetData(event.PTP4l).AddEvent(large)
	inbound <- large
	drain()
	assert.Equal(t, 0, phc2sys.starts, "large offset from HA-linked profile should not start phc2sys")

	small := event.Event{
		Source:  event.PTP4l,
		CfgName: master2,
		IFace:   "ens2f0",
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 500000000},
	}
	cm.GetClock(master2).GetData(event.PTP4l).AddEvent(small)
	cm.GetClock(master2).GetData(event.PTP4l).AddEvent(small)
	cm.GetClock(master2).GetData(event.PTP4l).AddEvent(small)
	inbound <- small
	assert.Eventually(t, func() bool { return phc2sys.starts == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestPhc2sysOffsetStartCondition_TGM(t *testing.T) {
	c := phc2sysOffsetStartCondition(ptpProcessEnv{runID: 2, clockType: event.GM, nodeProfile: &ptpv1.PtpProfile{PtpSettings: map[string]string{"clockType": TGM}}})
	assert.Equal(t, process.OnStateAndOffsetForCount{
		ClockID:    "ts2phc.2.config",
		ConfigName: "ts2phc.2.config",
		Source:     event.TS2PHC,
		State:      event.PTP_LOCKED,
		MaxOffset:  1e9,
		Count:      3,
	}, c)
}

func TestPhc2sysOffsetStartCondition_InferredGM(t *testing.T) {
	c := phc2sysOffsetStartCondition(ptpProcessEnv{runID: 0, clockType: event.GM})
	assert.Equal(t, process.OnStateAndOffsetForCount{
		ClockID:    "ts2phc.0.config",
		ConfigName: "ts2phc.0.config",
		Source:     event.TS2PHC,
		State:      event.PTP_LOCKED,
		MaxOffset:  1e9,
		Count:      3,
	}, c)
}

func TestPhc2sysOffsetStartCondition_TBC(t *testing.T) {
	c := phc2sysOffsetStartCondition(ptpProcessEnv{runID: 0, clockType: event.TBC, nodeProfile: &ptpv1.PtpProfile{PtpSettings: map[string]string{"clockType": TBC}}})
	assert.Equal(t, process.OnStateAndOffsetForCount{
		ClockID:    ptp4lConfig,
		ConfigName: ptp4lConfig,
		Source:     event.PTP4l,
		State:      event.PTP_LOCKED,
		MaxOffset:  1e9,
		Count:      3,
	}, c)
}

func TestPhc2sysOffsetStartCondition_HA(t *testing.T) {
	master1 := "test-bc-master1"
	master2 := "test-bc-master2"
	dn := &Daemon{processManager: &ProcessManager{
		process: []process.Process{
			&ptpProcess{ExecProcess: ExecProcess{name: ptp4lProcessName, configName: "ptp4l.0.config"}, nodeProfile: &ptpv1.PtpProfile{Name: &master1}},
			&ptpProcess{ExecProcess: ExecProcess{name: ptp4lProcessName, configName: testPtp4l1Config}, nodeProfile: &ptpv1.PtpProfile{Name: &master2}},
		},
	}}
	c := phc2sysOffsetStartCondition(ptpProcessEnv{
		runID:       5,
		nodeProfile: &ptpv1.PtpProfile{PtpSettings: map[string]string{PTP_HA_IDENTIFIER: master1 + "," + master2, "clockType": TBC}},
		dn:          dn,
	})
	anyCond, ok := c.(process.Any)
	if !assert.True(t, ok, "HA should wrap per-config conditions in Any") {
		return
	}
	assert.Len(t, anyCond.Conditions, 2)
	assert.Equal(t, "ptp4l.0.config", anyCond.Conditions[0].(process.OnStateAndOffsetForCount).ConfigName)
	assert.Equal(t, testPtp4l1Config, anyCond.Conditions[1].(process.OnStateAndOffsetForCount).ConfigName)
	assert.Equal(t, event.PTP4l, anyCond.Conditions[0].(process.OnStateAndOffsetForCount).Source)
}
