package daemon

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/addons/generic"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const (
	testETH0         = "eth0"
	testE810         = "e810"
	testPtp4l1Config = "ptp4l.1.config"
	clockTypeSetting = "clockType"
	testTs2phcConfig = "ts2phc.0.config"
	ntpfailover      = "ntpfailover"
)

type stubProcess struct {
	name      string
	cfgName   string
	clockType event.ClockType
	iface     string
	state     process.State
	starts    int
	stops     int
	deps      []process.Process
	conds     map[process.Action]process.Condition
	profile   *ptpv1.PtpProfile
	mu        sync.RWMutex
}

func (s *stubProcess) Name() string {
	return s.name
}

func (s *stubProcess) ConfigName() string { return s.cfgName }
func (s *stubProcess) SyncInitialState()  {}
func (s *stubProcess) Start(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts++
	s.state = process.Running
	return nil
}
func (s *stubProcess) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stops++
	s.state = process.Stopped
	return nil
}
func (s *stubProcess) Starts() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.starts
}
func (s *stubProcess) Stops() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stops
}
func (s *stubProcess) Conditions() map[process.Action]process.Condition {
	return s.conds
}
func (s *stubProcess) State() process.State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}
func (s *stubProcess) Profile() *ptpv1.PtpProfile { return s.profile }
func (s *stubProcess) ClockType() event.ClockType {
	if s.clockType == "" {
		return event.OC
	}
	return s.clockType
}
func (s *stubProcess) DependentProcesses() []process.Process {
	return s.deps
}
func (s *stubProcess) IFace() string { return s.iface }

func newStubProcess(p process.Process) *stubProcess {
	if p == nil {
		return nil
	}
	var deps []process.Process
	for _, d := range p.DependentProcesses() {
		deps = append(deps, newStubProcess(d))
	}
	return &stubProcess{
		name:    p.Name(),
		cfgName: p.ConfigName(),
		state:   p.State(),
		conds:   p.Conditions(),
		profile: p.Profile(),
		deps:    deps,
	}
}

func stringPtr(s string) *string { return &s }

type ConditionsTester struct {
	dn     *Daemon
	pm     *ProcessManager
	cm     *clockmgr.ClockManager
	clks   map[string]*clock.Clock
	ctx    context.Context
	cancel context.CancelFunc
	t      *testing.T
}

func NewCondiitonsTester(t *testing.T, clockType event.ClockType, cfgNames ...string) *ConditionsTester {
	t.Helper()

	origPrefix := configPrefix
	configPrefix = t.TempDir()
	t.Cleanup(func() { configPrefix = origPrefix })

	inbound := make(chan event.Event, 20)
	handler := make(chan event.Event, 20)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	c := &ConditionsTester{
		t:      t,
		ctx:    ctx,
		cancel: cancel,
		pm: &ProcessManager{
			eventsIn:  inbound,
			eventsOut: handler,
		},
		clks: make(map[string]*clock.Clock),
	}

	cm := clockmgr.Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	pm := c.pm
	pm.clockMgr = cm
	dn := &Daemon{
		processManager: pm,
	}
	pm.daemon = dn
	c.dn = dn
	c.cm = cm

	for _, n := range cfgNames {
		var (
			clk clock.Clock
			err error
		)

		switch clockType {
		case event.GM:
			clk, err = clock.NewGM(n, cm.GetUtcOffset, &pmc.MockClient{})
			assert.NoError(t, err)
		case event.TBC:
			clk, err = clock.NewTBC(n, cm.GetUtcOffset, &pmc.MockClient{})
			assert.NoError(t, err)
		case event.BC:
			clk, err = clock.NewBC(n, false, event.PtpClockThreshold{})
			assert.NoError(t, err)
		default:
			clk, err = clock.NewBC(n, true, event.PtpClockThreshold{})
			assert.NoError(t, err)
		}
		err = cm.AddClock(clk)
		assert.NoError(t, err)

		c.clks[n] = &clk
	}

	return c
}

func (c *ConditionsTester) Daemon() *Daemon {
	return c.dn
}

func (c *ConditionsTester) Env(runID int, profile *ptpv1.PtpProfile, clockType event.ClockType, oSClockConfigs *OSClockConfigs) ptpProcessEnv {
	return ptpProcessEnv{
		runID:          runID,
		nodeProfile:    profile,
		clockType:      clockType,
		dn:             c.dn,
		hasFailover:    profile.Plugins != nil && profile.Plugins[ntpfailover] != nil,
		osClockConfigs: oSClockConfigs,
	}
}

func (c *ConditionsTester) AddProcess(p process.Process) *stubProcess {
	s, ok := p.(*stubProcess)
	if !ok {
		s = newStubProcess(p)
	}
	c.pm.process = append(c.pm.process, s)
	return s
}

func (c *ConditionsTester) StartPM() {
	c.pm.processOnce.Do(func() {
		go c.pm.processEvents(c.ctx)
	})
}

func (c *ConditionsTester) StartCM() {
	go func() {
		for {
			select {
			case <-c.ctx.Done():
				return
			case _, ok := <-c.pm.eventsOut:
				if !ok {
					return
				}
			}
		}
	}()
}

func (c *ConditionsTester) Start() {
	c.StartPM()
	c.StartCM()
}

func (c *ConditionsTester) StartProcesses() {
	c.pm.StartProcesses(c.ctx)
}

func (c *ConditionsTester) sendEvent(ev event.Event) {
	c.pm.eventsIn <- ev
	time.Sleep(5 * time.Millisecond)
}

func (c *ConditionsTester) SendProcessStatus(source event.EventSource, cfgName string, status int64) {
	c.sendEvent(event.ProcessStatusEvent(source, cfgName, event.OC, "", status))
}

func (c *ConditionsTester) SendPluginEvent(plugin, eventName string) {
	c.sendEvent(event.PluginEvent(event.EventSource(plugin), eventName))
}

func (c *ConditionsTester) SendOffsetClock(source event.EventSource, clockID, cfgName string, state event.PTPState, offset int64) {
	ev := event.Event{
		Source:  source,
		CfgName: cfgName,
		IFace:   testETH0,
		Data: &event.OffsetData{
			State:  state,
			Offset: offset,
		},
	}
	if clk := c.cm.GetClock(clockID); clk != nil {
		clk.GetData(source).AddEvent(ev)
	} else if c.clks != nil {
		(*c.clks[cfgName]).GetData(source).AddEvent(ev)
	}
	c.sendEvent(ev)
}

func (c *ConditionsTester) SendOffset(source event.EventSource, cfgName string, state event.PTPState, offset int64) {
	c.SendOffsetClock(source, cfgName, cfgName, state, offset)
}

func (c *ConditionsTester) SendOffsetSamples(source event.EventSource, cfgName string, state event.PTPState, offset int64, count int) {
	for i := 0; i < count; i++ {
		c.SendOffset(source, cfgName, state, offset)
	}
}

func (c *ConditionsTester) SendOffsetClockSamples(source event.EventSource, clockID, cfgName string, state event.PTPState, offset int64, count int) {
	for i := 0; i < count; i++ {
		c.SendOffsetClock(source, clockID, cfgName, state, offset)
	}
}

func applyProfilesWithStubProcesses(t *testing.T, ct *ConditionsTester, files ...string) ([]*stubProcess, []ptpv1.PtpProfile) {
	t.Helper()
	RegisterMetrics("test-node")

	profiles := make([]ptpv1.PtpProfile, len(files))
	for i, file := range files {
		profile, err := loadProfile("testdata/" + file)
		require.NoError(t, err)
		profile.PtpSettings["unitTest"] = t.TempDir()
		profiles[i] = *profile
	}

	dn := NewDaemonForTests(&ReadyTracker{}, ct.pm)
	dn.processManager.clockMgr = ct.cm
	dn.processManager.daemon = dn
	ct.dn = dn
	t.Cleanup(dn.cancel)

	osClockConfigs := NewOSClockConfigs(profiles)
	controlledRunIDs := reconcileRelatedProfiles(profiles)
	for runID := range profiles {
		profile := &profiles[runID]
		if controlledID, ok := controlledRunIDs[*profile.Name]; ok {
			profile.PtpSettings["controlledId"] = strconv.Itoa(controlledID)
		}
		require.NoError(t, dn.applyNodePtpProfile(runID, profile, &osClockConfigs))
	}

	realProcesses := ct.pm.process
	ct.pm.process = nil
	var processes []*stubProcess
	for _, p := range realProcesses {
		root := ct.AddProcess(p)
		collectStubProcesses(root, &processes, make(map[*stubProcess]bool))
	}
	return processes, profiles
}

func collectStubProcesses(p *stubProcess, out *[]*stubProcess, seen map[*stubProcess]bool) {
	if p == nil || seen[p] {
		return
	}
	seen[p] = true
	*out = append(*out, p)
	for _, dep := range p.deps {
		if stub, ok := dep.(*stubProcess); ok {
			collectStubProcesses(stub, out, seen)
		}
	}
}

func assertRejectedStartupOffsets(t *testing.T, ct *ConditionsTester, p *stubProcess, source event.EventSource, cfgName string) {
	t.Helper()
	ct.SendOffset(event.GPSD, cfgName, event.PTP_LOCKED, 0)
	ct.SendOffset(source, cfgName, event.PTP_FREERUN, 0)
	ct.SendOffset(source, cfgName, event.PTP_LOCKED, int64(1<<63-1))
	assert.Zero(t, p.Starts(), "%s started on wrong-source, unlocked, or out-of-range samples", p.Name())
}

func findStubProcess(processes []*stubProcess, name string) *stubProcess {
	for _, p := range processes {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

func findStubProcessForProfile(processes []*stubProcess, name, profileName string) *stubProcess {
	for _, p := range processes {
		if p.Name() == name && p.Profile() != nil && p.Profile().Name != nil && *p.Profile().Name == profileName {
			return p
		}
	}
	return nil
}

func assertConstructedProcessesRunning(t *testing.T, processes []*stubProcess) {
	t.Helper()
	require.NotEmpty(t, processes, "profile must construct processes")
	assert.Eventually(t, func() bool {
		for _, p := range processes {
			if p.State() != process.Running {
				return false
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond, "constructed process graph did not start")
	for _, p := range processes {
		assert.Equal(t, 1, p.Starts(), "%s should start exactly once", p.Name())
	}
}

func TestApplyNodePtpProfileStartupConditions(t *testing.T) {
	t.Run("T-GM", func(t *testing.T) {
		ct := NewCondiitonsTester(t, event.GM)
		processes, _ := applyProfilesWithStubProcesses(t, ct, "profile-tgm.yaml")
		ts2phc := findStubProcess(processes, ts2phcProcessName)
		phc2sys := findStubProcess(processes, phc2sysProcessName)
		require.NotNil(t, ts2phc)
		require.NotNil(t, phc2sys)

		ct.StartProcesses()
		ct.StartCM()
		assert.Zero(t, ts2phc.Starts(), "T-GM ts2phc must wait for its constructed process-up condition")
		gpsd := findStubProcess(processes, GPSD_PROCESSNAME)
		gpspipe := findStubProcess(processes, GPSPIPE_PROCESSNAME)
		require.NotNil(t, gpsd)
		require.NotNil(t, gpspipe)
		ct.SendProcessStatus(event.GPSD, gpsd.ConfigName(), PtpProcessUp)
		ct.SendProcessStatus(event.GPSPIPE, gpspipe.ConfigName(), PtpProcessUp)
		assert.Eventually(t, func() bool { return ts2phc.State() == process.Running }, 2*time.Second, 10*time.Millisecond)

		assertRejectedStartupOffsets(t, ct, phc2sys, event.TS2PHC, ts2phc.ConfigName())
		ct.SendOffsetSamples(event.TS2PHC, ts2phc.ConfigName(), event.PTP_LOCKED, 0, 5)
		assertConstructedProcessesRunning(t, processes)
	})

	t.Run("T-BC", func(t *testing.T) {
		ct := NewCondiitonsTester(t, event.TBC)
		processes, profiles := applyProfilesWithStubProcesses(t, ct, "profile-tbc-tt.yaml", "profile-tbc-tr.yaml")
		ttProfileName := *profiles[0].Name
		trProfileName := *profiles[1].Name
		phc2sys := findStubProcessForProfile(processes, phc2sysProcessName, trProfileName)
		ts2phc := findStubProcessForProfile(processes, ts2phcProcessName, trProfileName)
		ptp4l := findStubProcessForProfile(processes, ptp4lProcessName, trProfileName)
		require.NotNil(t, phc2sys)
		require.NotNil(t, ts2phc)
		require.NotNil(t, ptp4l)
		require.NotNil(t, findStubProcessForProfile(processes, ptp4lProcessName, ttProfileName))

		ct.StartProcesses()
		ct.StartCM()
		assert.Zero(t, phc2sys.Starts())
		assert.Zero(t, ts2phc.Starts())
		for _, ptp4lProcess := range processes {
			if ptp4lProcess.Name() == ptp4lProcessName {
				ct.SendProcessStatus(event.PTP4l, ptp4lProcess.ConfigName(), PtpProcessUp)
			}
		}

		assertRejectedStartupOffsets(t, ct, phc2sys, event.PTP4l, ptp4l.ConfigName())
		ct.SendOffsetSamples(event.PTP4l, ptp4l.ConfigName(), event.PTP_LOCKED, 0, 5)
		assert.Eventually(t, func() bool { return phc2sys.State() == process.Running }, 2*time.Second, 10*time.Millisecond)

		assertRejectedStartupOffsets(t, ct, ts2phc, event.PHC2SYS, ptp4l.ConfigName())
		ct.SendOffsetSamples(event.PTP4l, ptp4l.ConfigName(), event.PTP_LOCKED, 0, 5)
		ct.SendOffsetSamples(event.PHC2SYS, ptp4l.ConfigName(), event.PTP_LOCKED, 0, 5)
		assertConstructedProcessesRunning(t, processes)
	})
}

func TestForwardEvents_HopsEvent(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC, ptp4lConfig)
	ct.StartPM()

	want := event.Event{
		Source:    event.PTP4l,
		CfgName:   ptp4lConfig,
		ClockType: event.OC,
		Reset:     true,
	}
	ct.sendEvent(want)

	select {
	case got := <-ct.pm.eventsOut:
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
	ct := NewCondiitonsTester(t, event.OC, ptp4lConfig)
	stub := ct.AddProcess(&stubProcess{name: string(event.PTP4l), cfgName: ptp4lConfig, state: process.Dead})
	ct.StartPM()

	ct.SendProcessStatus(event.PTP4l, ptp4lConfig, PtpProcessDown)

	assert.Eventually(t, func() bool { return stub.Starts() == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Empty(t, ct.pm.eventsOut, "ProcessStatusData must not be forwarded to handler")
}

func TestForwardEvents_ProcessStatusDownStoppedNoRestart(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC, ptp4lConfig)
	stub := ct.AddProcess(&stubProcess{name: string(event.PTP4l), state: process.Stopped})
	ct.StartPM()

	ct.SendProcessStatus(event.PTP4l, ptp4lConfig, PtpProcessDown)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, stub.starts)
	assert.Empty(t, ct.pm.eventsOut, "ProcessStatusData must not be forwarded to handler")
}

func TestForwardEvents_ProcessStatusDownRestartsDep(t *testing.T) {
	ct := NewCondiitonsTester(t, event.GM, testTs2phcConfig)
	dep := &stubProcess{name: string(event.GPSD), cfgName: testTs2phcConfig, state: process.Dead}
	parent := ct.AddProcess(&stubProcess{name: string(event.TS2PHC), cfgName: testTs2phcConfig, state: process.Running, deps: []process.Process{dep}})
	ct.StartPM()

	ct.SendProcessStatus(event.GPSD, testTs2phcConfig, PtpProcessDown)

	assert.Eventually(t, func() bool { return dep.Starts() == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 0, parent.starts)
	assert.Empty(t, ct.pm.eventsOut, "ProcessStatusData must not be forwarded to handler")
}

func TestStartProcesses_MissingConditionStartsImmediately(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	dep := &stubProcess{name: "gpspipe"}
	parent := ct.AddProcess(&stubProcess{name: "ts2phc", deps: []process.Process{dep}})

	ct.StartProcesses()

	assert.Equal(t, 1, dep.starts)
	assert.Equal(t, 1, parent.starts)
}

func TestStartProcesses_AlreadyRunningNotRestarted(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	parent := ct.AddProcess(&stubProcess{
		name:  "ts2phc",
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.GPSD, ConfigName: "ts2phc.0.config"},
		},
	})
	ct.SendProcessStatus(event.GPSD, "ts2phc.0.config", PtpProcessUp)
	time.Sleep(10 * time.Millisecond)
	assert.Equal(t, 0, parent.starts)
}

func TestForwardEvents_OnProcessUpStartsCreated(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	pmc := &stubProcess{
		name: "pmc",
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.PTP4l, ConfigName: ptp4lConfig},
		},
	}
	parent := ct.AddProcess(&stubProcess{name: string(event.PTP4l), state: process.Running, deps: []process.Process{pmc}})
	ct.StartPM()

	ct.SendProcessStatus(event.PTP4l, ptp4lConfig, PtpProcessUp)

	assert.Eventually(t, func() bool { return pmc.Starts() == 1 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 0, parent.starts)
}

func TestForwardEvents_OnProcessUpWrongSourceNoStart(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	pmc := &stubProcess{
		name: "pmc",
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{Source: event.PTP4l, ConfigName: ptp4lConfig},
		},
	}
	ct.AddProcess(&stubProcess{name: string(event.PTP4l), state: process.Running, deps: []process.Process{pmc}})
	ct.StartPM()

	ct.SendProcessStatus(event.GPSD, ptp4lConfig, PtpProcessUp)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, pmc.starts)

	ct.SendProcessStatus(event.PTP4l, ptp4lConfig, PtpProcessDown)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, pmc.starts)
}

func TestStartProcesses_ImmediateDoesNotCauseProcessToBeRestartedWhileRunning(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	parent := ct.AddProcess(&stubProcess{name: string(event.PTP4l)})
	ct.StartProcesses()
	assert.Equal(t, 1, parent.starts)

	ct.StartPM()
	ct.SendProcessStatus(event.PTP4l, ptp4lConfig, PtpProcessUp)
	assert.Eventually(t, func() bool { return parent.starts == 1 }, 5*time.Second, 1*time.Millisecond)
	assert.Empty(t, ct.pm.eventsOut, "ProcessStatusData must not be forwarded to handler")
}

func TestEvalActions_StopRunningProcess(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	phc2sys := ct.AddProcess(&stubProcess{
		name:  phc2sysProcessName,
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStop: process.OnPluginEvent{EventName: generic.GnssRecoveredEventName},
		},
	})
	ct.StartPM()

	ct.SendPluginEvent(ntpfailover, generic.GnssRecoveredEventName)

	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 1, phc2sys.stops)
	assert.Equal(t, process.Stopped, phc2sys.state)
}

func TestEvalActions_StartStoppedProcess(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	phc2sys := ct.AddProcess(&stubProcess{
		name:  phc2sysProcessName,
		state: process.Stopped,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: generic.GnssRecoveredEventName},
		},
	})
	ct.Start()

	ct.SendPluginEvent(ntpfailover, generic.GnssRecoveredEventName)

	assert.Eventually(t, func() bool { return phc2sys.Starts() == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestEvalActions_StartStopChronyD(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	chronyd := ct.AddProcess(&stubProcess{
		name:  "chronyd",
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: generic.GnssFailoverEventName},
			process.ActionStop:  process.OnPluginEvent{EventName: generic.GnssRecoveredEventName},
		},
	})

	ct.StartPM()

	ct.SendPluginEvent(ntpfailover, generic.GnssFailoverEventName)
	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 1, chronyd.starts)
	assert.Equal(t, 0, chronyd.stops)
	assert.Equal(t, process.Running, chronyd.state)

	ct.SendPluginEvent(ntpfailover, generic.GnssRecoveredEventName)
	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 1, chronyd.starts)
	assert.Equal(t, 1, chronyd.stops)
	assert.Equal(t, process.Stopped, chronyd.state)

	// Do it a second time to make sure it can handle a cycles
	ct.SendPluginEvent(ntpfailover, generic.GnssFailoverEventName)
	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 2, chronyd.starts)
	assert.Equal(t, 1, chronyd.stops)
	assert.Equal(t, process.Running, chronyd.state)

	ct.SendPluginEvent(ntpfailover, generic.GnssRecoveredEventName)
	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 2, chronyd.starts)
	assert.Equal(t, 2, chronyd.stops)
	assert.Equal(t, process.Stopped, chronyd.state)
}

func TestEvalActions_StartNotCheckedWhenRunning(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	phc2sys := ct.AddProcess(&stubProcess{
		name:  phc2sysProcessName,
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: generic.GnssRecoveredEventName},
		},
	})
	ct.StartPM()

	ct.SendPluginEvent(ntpfailover, generic.GnssRecoveredEventName)
	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 0, phc2sys.starts)
}

func TestEvalActions_StartOnlyWhenStopped(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	chronyd := ct.AddProcess(&stubProcess{
		name:  "chronyd",
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: generic.GnssFailoverEventName},
			process.ActionStop:  process.OnPluginEvent{EventName: generic.GnssRecoveredEventName},
		},
	})
	chronyd.state = process.Running
	ct.pm.process = append(ct.pm.process, chronyd)
	ct.StartPM()

	ct.SendPluginEvent(ntpfailover, generic.GnssFailoverEventName)
	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, 0, chronyd.starts)
	assert.Equal(t, 0, chronyd.stops)
}

func TestEvalActions_NoActionWithoutCondition(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	stub := ct.AddProcess(&stubProcess{name: ptp4lProcessName, state: process.Running})
	ct.StartPM()

	ct.SendPluginEvent(ntpfailover, generic.GnssRecoveredEventName)
	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("event was not forwarded")
	}
	assert.Equal(t, process.Running, stub.state)
}

func TestEvalActions_FailOverStart(t *testing.T) {
	inbound := make(chan event.Event, 4)
	handler := make(chan event.Event, 4)
	phc2sys := &stubProcess{
		name:  phc2sysProcessName,
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: generic.GnssRecoveredEventName},
			process.ActionStop:  process.OnPluginEvent{EventName: generic.GnssRecoveredEventName},
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

	pm.StartProcesses(ctx)
	assert.Equal(t, phc2sys.starts, 0, "phc2sys should start")
	inbound <- event.PluginEvent("ntpfailover", generic.GnssRecoveredEventName)
	select {
	case <-handler:
	case <-time.After(2 * time.Second):
		t.Fatal("failover event was not forwarded")
	}
	assert.Equal(t, phc2sys.starts, 1, "phc2sys should start")
}

func TestEvalActions_FullFailoverFlow(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC)
	phc2sys := ct.AddProcess(&stubProcess{
		name:  phc2sysProcessName,
		state: process.Running,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: generic.GnssRecoveredEventName},
			process.ActionStop:  process.OnPluginEvent{EventName: generic.GnssFailoverEventName},
		},
	})
	chronyd := ct.AddProcess(&stubProcess{
		name:  "chronyd",
		state: process.Created,
		conds: map[process.Action]process.Condition{
			process.ActionStart: process.OnPluginEvent{EventName: generic.GnssFailoverEventName},
			process.ActionStop:  process.OnPluginEvent{EventName: generic.GnssRecoveredEventName},
		},
	})
	ct.pm.process = append(ct.pm.process, chronyd)
	ct.StartPM()

	// Failover: stop phc2sys + Start chronyd
	ct.SendPluginEvent(ntpfailover, generic.GnssFailoverEventName)
	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("failover event was not forwarded")
	}
	assert.Equal(t, 1, chronyd.starts, "chronyd should be Startd on failover")

	// Simulate phc2sys being stopped
	phc2sys.state = process.Stopped

	// Recovery: start phc2sys + Stop chronyd
	ct.SendPluginEvent(ntpfailover, generic.GnssRecoveredEventName)
	assert.Eventually(t, func() bool { return phc2sys.Starts() == 1 }, 2*time.Second, 10*time.Millisecond)
	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("recovery event was not forwarded")
	}
	assert.Equal(t, 1, chronyd.stops, "chronyd should be Stopped on recovery")
}

func TestForwardEvents_AllStatefulStartsWhenAllConditionsMet(t *testing.T) {
	ct := NewCondiitonsTester(t, event.GM, testTs2phcConfig)
	parent := ct.AddProcess(&stubProcess{
		name: string(event.TS2PHC),
		conds: map[process.Action]process.Condition{
			process.ActionStart: &process.All{Conditions: []process.Condition{
				process.OnProcessUp{Source: event.GPSD},
				process.OnProcessUp{Source: event.GPSPIPE},
			}},
		},
	})
	ct.Start()

	ct.SendProcessStatus(event.GPSD, testTs2phcConfig, PtpProcessUp)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, parent.Starts(), "not yet started with only first condition met")

	ct.SendProcessStatus(event.GPSPIPE, testTs2phcConfig, PtpProcessUp)
	assert.Eventually(t, func() bool { return parent.Starts() == 1 }, 2*time.Second, 10*time.Millisecond, "stateful All starts when all conditions have been met (even at different times)")
}

func TestEvalActions_DelayedPhc2sysStartOnSubSecondOffset(t *testing.T) {
	const count = 3
	ct := NewCondiitonsTester(t, event.OC, testTs2phcConfig)
	phc2sys := ct.AddProcess(&stubProcess{
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
	})
	ct.Start()

	ct.SendOffset(event.TS2PHC, testTs2phcConfig, event.PTP_LOCKED, 37000000000)
	assert.Equal(t, 0, phc2sys.starts, "large offset should not start phc2sys")

	ct.SendOffset(event.TS2PHC, testTs2phcConfig, event.PTP_LOCKED, 1000000000)
	assert.Equal(t, 0, phc2sys.starts, "boundary offset should not start phc2sys")

	ct.SendOffset(event.TS2PHC, testTs2phcConfig, event.PTP_LOCKED, -2000000000)
	assert.Equal(t, 0, phc2sys.starts, "negative super-second offset should not start phc2sys")

	ct.SendOffsetSamples(event.TS2PHC, testTs2phcConfig, event.PTP_LOCKED, 100000, count)
	assert.Equal(t, 0, phc2sys.starts, "should not start yet, need > count samples")

	ct.SendOffset(event.TS2PHC, testTs2phcConfig, event.PTP_LOCKED, -5000)
	assert.Eventually(t, func() bool { return phc2sys.Starts() == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestEvalActions_DelayedPhc2sysWrongStateDoesNotStart(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC, testTs2phcConfig)
	phc2sys := ct.AddProcess(&stubProcess{
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
	})
	ct.Start()

	// Sub-second offset but FREERUN state should NOT start phc2sys
	ct.SendOffsetSamples(event.TS2PHC, testTs2phcConfig, event.PTP_FREERUN, 50000, 5)
	assert.Equal(t, 0, phc2sys.starts, "FREERUN state should not start phc2sys regardless of offset")
}

func TestEvalActions_DelayedPhc2sysTBCWaitsForPtp4l(t *testing.T) {
	const cfgName = "ptp4l.0.config"
	ct := NewCondiitonsTester(t, event.OC, cfgName, testTs2phcConfig)
	phc2sys := ct.AddProcess(&stubProcess{
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
	})
	ct.Start()

	// ts2phc events must not start phc2sys in T-BC
	ct.SendOffsetSamples(event.TS2PHC, testTs2phcConfig, event.PTP_LOCKED, 50000, 3)
	assert.Equal(t, 0, phc2sys.starts, "T-BC phc2sys must not start on ts2phc events")

	// ptp4l events start phc2sys in T-BC
	ct.SendOffsetSamples(event.PTP4l, cfgName, event.PTP_LOCKED, 50000, 3)
	assert.Eventually(t, func() bool { return phc2sys.Starts() == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestEvalActions_DelayedPhc2sysHAProfile(t *testing.T) {
	master1 := ptp4lConfig
	master2 := testPtp4l1Config
	ct := NewCondiitonsTester(t, event.BC, master1, master2)
	phc2sys := ct.AddProcess(&stubProcess{
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
	})
	ct.Start()

	ct.SendOffsetSamples(event.PTP4l, master1, event.PTP_LOCKED, 37000000000, 3)
	assert.Equal(t, 0, phc2sys.starts, "large offset from HA-linked profile should not start phc2sys")

	ct.SendOffsetSamples(event.PTP4l, master2, event.PTP_LOCKED, 500000000, 3)
	assert.Eventually(t, func() bool { return phc2sys.Starts() == 1 }, 2*time.Second, 10*time.Millisecond)
}

func TestPhc2sysOffsetStartCondition_TGM(t *testing.T) {
	profile := ptpv1.PtpProfile{PtpSettings: map[string]string{clockTypeSetting: TGM}}
	c := phc2sysOffsetStartCondition(ptpProcessEnv{runID: 2, clockType: event.GM, nodeProfile: &profile})
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
	profile := ptpv1.PtpProfile{PtpSettings: map[string]string{clockTypeSetting: TBC}}
	oSClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{profile})
	c := phc2sysOffsetStartCondition(ptpProcessEnv{runID: 0, clockType: event.TBC, nodeProfile: &profile, osClockConfigs: &oSClockConfigs})
	assert.Equal(t, process.OnStateAndOffsetForCount{
		ClockID:    "ptp4l.0.config",
		ConfigName: "ptp4l.0.config",
		Source:     event.PTP4l,
		State:      event.PTP_LOCKED,
		MaxOffset:  1e9,
		Count:      3,
	}, c)
}

func TestPhc2sysOffsetStartCondition_HA(t *testing.T) {
	master1 := "test-bc-master1"
	master2 := "test-bc-master2"

	profile1 := ptpv1.PtpProfile{Name: &master1}
	profile2 := ptpv1.PtpProfile{Name: &master2}
	haProfile := ptpv1.PtpProfile{
		Phc2sysConf: event.Ptr(" "),
		Phc2sysOpts: event.Ptr(" "),
		PtpSettings: map[string]string{PTP_HA_IDENTIFIER: master1 + "," + master2,
			clockTypeSetting: TBC,
		}}

	oSClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{profile1, profile2, haProfile})

	ct := NewCondiitonsTester(t, event.OC)
	ct.AddProcess(&ptpProcess{ExecProcess: ExecProcess{name: ptp4lProcessName, configName: "ptp4l.0.config"}, nodeProfile: &profile1})
	ct.AddProcess(&ptpProcess{ExecProcess: ExecProcess{name: ptp4lProcessName, configName: "ptp4l.1.config"}, nodeProfile: &profile2})

	c := phc2sysOffsetStartCondition(ct.Env(5, &haProfile, event.TBC, &oSClockConfigs))
	anyCond, ok := c.(process.Any)
	if !assert.True(t, ok, "HA should wrap per-config conditions in Any") {
		return
	}
	assert.Len(t, anyCond.Conditions, 2)
	assert.Equal(t, "ptp4l.0.config", anyCond.Conditions[0].(process.OnStateAndOffsetForCount).ConfigName)
	assert.Equal(t, "ptp4l.1.config", anyCond.Conditions[1].(process.OnStateAndOffsetForCount).ConfigName)
	assert.Equal(t, event.PTP4l, anyCond.Conditions[0].(process.OnStateAndOffsetForCount).Source)
}

func TestTBC_Ts2phcUnlocksThroughProcessManager(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC, ptp4lConfig, "ts2phc.0.config")

	profileName := "test-tbc"
	profile := &ptpv1.PtpProfile{
		Name: &profileName,
		PtpSettings: map[string]string{
			clockTypeSetting: TBC,
		},
		Ts2PhcConf: stringPtr("[global]\n[ts2phc.0.config]\n"),
		Ts2PhcOpts: stringPtr("-s"),
	}

	osClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{*profile})

	realTs2phc, err := NewTs2phcProcess(ct.Env(0, profile, event.TBC, &osClockConfigs))
	require.NoError(t, err)
	ts2phcStub := ct.AddProcess(realTs2phc)

	ct.StartProcesses()
	assert.Equal(t, 0, ts2phcStub.Starts(), "T-BC ts2phc must not start immediately")

	ct.Start()

	// FREERUN -> must not start
	ct.SendOffset(event.PTP4l, ptp4lConfig, event.PTP_FREERUN, 50000)
	assert.Equal(t, 0, ts2phcStub.Starts(), "must not start on FREERUN")

	// Super-second offset (> 1s) -> must not start
	ct.SendOffset(event.PTP4l, ptp4lConfig, event.PTP_LOCKED, 2000000000)
	assert.Equal(t, 0, ts2phcStub.Starts(), "must not start on super-second offset")

	// 3 in-spec samples (need > 3 samples)
	ct.SendOffsetSamples(event.PTP4l, ptp4lConfig, event.PTP_LOCKED, 10000, 3)
	assert.Equal(t, 0, ts2phcStub.Starts(), "must not start yet, need > 3 samples in window")

	// 4th in-spec sample -> ts2phc unlocks and starts!
	ct.SendOffset(event.PTP4l, ptp4lConfig, event.PTP_LOCKED, 10000)
	assert.Eventually(t, func() bool { return ts2phcStub.Starts() == 1 }, 2*time.Second, 10*time.Millisecond,
		"T-BC ts2phc must unlock and start once ptp4l lock and offset threshold are satisfied")

	assert.Equal(t,
		[]process.WindowRequest{
			{ClockID: ptp4lConfig, Source: event.PTP4l},
		},
		realTs2phc.Conditions()[process.ActionStart].GetWindowRequests(),
		"Incorrect Window Requests",
	)
}

func TestTBC_Ts2phcWithPhc2sysUnlocksThroughProcessManager(t *testing.T) {
	ct := NewCondiitonsTester(t, event.OC, ptp4lConfig, "ts2phc.0.config")

	profileName := "test-tbc-phc2sys"
	profile := &ptpv1.PtpProfile{
		Name: &profileName,
		PtpSettings: map[string]string{
			clockTypeSetting: TBC,
		},
		Ts2PhcConf:  stringPtr("[global]\n[ts2phc.0.config]\n"),
		Ts2PhcOpts:  stringPtr("-s"),
		Phc2sysConf: stringPtr("[global]\n"),
	}

	osClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{*profile})

	realTs2phc, err := NewTs2phcProcess(ct.Env(0, profile, event.TBC, &osClockConfigs))
	require.NoError(t, err)
	ts2phcStub := ct.AddProcess(realTs2phc)

	ct.StartProcesses()
	assert.Equal(t, 0, ts2phcStub.Starts())

	ct.Start()

	// Qualify ptp4l with 5 samples (> 3)
	ct.SendOffsetSamples(event.PTP4l, ptp4lConfig, event.PTP_LOCKED, 10000, 5)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, ts2phcStub.Starts(), "ts2phc must not start with only ptp4l locked; still waiting for phc2sys")

	// Qualify phc2sys with 5 samples (> 3)
	ct.SendOffsetSamples(event.PHC2SYS, ptp4lConfig, event.PTP_LOCKED, 15000, 5)

	// Stateful All condition should now be fully satisfied
	assert.Eventually(t, func() bool { return ts2phcStub.Starts() == 1 }, 2*time.Second, 10*time.Millisecond,
		"T-BC ts2phc must start once both ptp4l and phc2sys conditions are met")

	assert.Equal(t,
		[]process.WindowRequest{
			{ClockID: ptp4lConfig, Source: event.PTP4l},
			{ClockID: ptp4lConfig, Source: event.PHC2SYS},
		},
		realTs2phc.Conditions()[process.ActionStart].GetWindowRequests(),
		"Incorrect Window Requests",
	)
}

func TestUnlock_TBC_Phc2sys(t *testing.T) {
	ct := NewCondiitonsTester(t, event.TBC, ptp4lConfig)

	profileName := "test-tbc-phc2sys"
	profile := &ptpv1.PtpProfile{
		Name: &profileName,
		PtpSettings: map[string]string{
			clockTypeSetting: TBC,
		},
		Phc2sysConf: stringPtr("[global]\n"),
		Phc2sysOpts: stringPtr("-a -r"),
	}
	osClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{*profile})

	realPhc2sys, err := NewPhc2sysProcess(ct.Env(0, profile, event.TBC, &osClockConfigs))
	require.NoError(t, err)
	stub := ct.AddProcess(realPhc2sys)

	ct.StartProcesses()
	assert.Equal(t, 0, stub.Starts(), "T-BC phc2sys must not start immediately")

	ct.Start()

	// FREERUN -> stays 0
	ct.SendOffset(event.PTP4l, ptp4lConfig, event.PTP_FREERUN, 50000)
	assert.Equal(t, 0, stub.Starts())

	// Large offset -> stays 0
	ct.SendOffset(event.PTP4l, ptp4lConfig, event.PTP_LOCKED, 2000000000)
	assert.Equal(t, 0, stub.Starts())

	// 5 in-spec samples (> 3) -> unlocks!
	ct.SendOffsetSamples(event.PTP4l, ptp4lConfig, event.PTP_LOCKED, 10000, 5)
	assert.Eventually(t, func() bool { return stub.Starts() == 1 }, 2*time.Second, 10*time.Millisecond,
		"T-BC phc2sys must unlock and start once ptp4l lock is confirmed")
}

func TestUnlock_TGM_Phc2sys(t *testing.T) {
	ct := NewCondiitonsTester(t, event.GM, "ts2phc.0.config")

	profileName := "test-tgm-phc2sys"
	profile := &ptpv1.PtpProfile{
		Name: &profileName,
		PtpSettings: map[string]string{
			clockTypeSetting: TGM,
		},
		Phc2sysConf: stringPtr("[global]\n"),
		Phc2sysOpts: stringPtr("-a -r"),
	}

	osClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{*profile})

	realPhc2sys, err := NewPhc2sysProcess(ct.Env(0, profile, event.GM, &osClockConfigs))
	require.NoError(t, err)
	stub := ct.AddProcess(realPhc2sys)

	ct.StartProcesses()
	assert.Equal(t, 0, stub.Starts(), "T-GM phc2sys must not start immediately")

	ct.Start()

	// FREERUN -> stays 0
	ct.SendOffset(event.TS2PHC, "ts2phc.0.config", event.PTP_FREERUN, 50000)
	assert.Equal(t, 0, stub.Starts())

	// 5 in-spec samples (> 3) -> unlocks!
	ct.SendOffsetSamples(event.TS2PHC, "ts2phc.0.config", event.PTP_LOCKED, 10000, 5)
	assert.Eventually(t, func() bool { return stub.Starts() == 1 }, 2*time.Second, 10*time.Millisecond,
		"T-GM phc2sys must unlock and start once ts2phc lock is confirmed")
}

func TestUnlock_BC_Phc2sys(t *testing.T) {
	ct := NewCondiitonsTester(t, event.BC, ptp4lConfig)

	profileName := "test-bc-phc2sys"
	profile := &ptpv1.PtpProfile{
		Name: &profileName,
		PtpSettings: map[string]string{
			clockTypeSetting: string(event.BC),
		},
		Phc2sysConf: stringPtr("[global]\n"),
		Phc2sysOpts: stringPtr("-a -r"),
	}
	osClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{*profile})

	realPhc2sys, err := NewPhc2sysProcess(ct.Env(0, profile, event.BC, &osClockConfigs))
	require.NoError(t, err)
	stub := ct.AddProcess(realPhc2sys)

	ct.StartProcesses()
	assert.Equal(t, 0, stub.Starts(), "BC phc2sys must not start immediately")

	ct.Start()

	// 5 in-spec samples (> 3) -> unlocks!
	ct.SendOffsetSamples(event.PTP4l, ptp4lConfig, event.PTP_LOCKED, 10000, 5)
	assert.Eventually(t, func() bool { return stub.Starts() == 1 }, 2*time.Second, 10*time.Millisecond,
		"BC phc2sys must unlock and start once ptp4l lock is confirmed")
}

func TestUnlock_HA_Phc2sys(t *testing.T) {
	ct := NewCondiitonsTester(t, event.BC, ptp4lConfig, testPtp4l1Config)

	master1 := "test-ha-master1"
	master2 := "test-ha-master2"
	profileName := "test-ha-tbc"
	profile := &ptpv1.PtpProfile{
		Name: &profileName,
		PtpSettings: map[string]string{
			PTP_HA_IDENTIFIER: master1 + "," + master2,
			clockTypeSetting:  TBC,
		},
		Phc2sysConf: stringPtr("[global]\n"),
		Phc2sysOpts: stringPtr("-a -r"),
	}

	// Register HA masters in the unified ProcessManager
	ct.AddProcess(&ptpProcess{ExecProcess: ExecProcess{name: ptp4lProcessName, configName: "ptp4l.0.config"}, nodeProfile: &ptpv1.PtpProfile{Name: &master1}})
	ct.AddProcess(&ptpProcess{ExecProcess: ExecProcess{name: ptp4lProcessName, configName: testPtp4l1Config}, nodeProfile: &ptpv1.PtpProfile{Name: &master2}})

	osClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{*profile})

	realPhc2sys, err := NewPhc2sysProcess(ct.Env(0, profile, event.TBC, &osClockConfigs))
	require.NoError(t, err)
	stub := ct.AddProcess(realPhc2sys)

	ct.StartProcesses()
	assert.Equal(t, 0, stub.Starts())

	ct.Start()

	// Master 1 qualifies with 5 samples (> 3) -> phc2sys unlocks via Any
	ct.SendOffsetSamples(event.PTP4l, ptp4lConfig, event.PTP_LOCKED, 10000, 5)
	assert.Eventually(t, func() bool { return stub.Starts() == 1 }, 2*time.Second, 10*time.Millisecond,
		"HA phc2sys must unlock when Master 1 achieves lock")
}

func TestUnlock_Failover_Phc2sysAndChronyd(t *testing.T) {
	ct := NewCondiitonsTester(t, event.GM, "ts2phc.0.config")

	profileName := "test-failover"
	profile := &ptpv1.PtpProfile{
		Plugins: map[string]*v1.JSON{
			ntpfailover: {
				Raw: []byte("true"),
			},
		},
		Name: &profileName,
		PtpSettings: map[string]string{
			clockTypeSetting: TGM,
		},
		Phc2sysConf: stringPtr("[global]\n"),
		Phc2sysOpts: stringPtr("-a -r"),
		ChronydConf: stringPtr("[global]\n"),
		ChronydOpts: stringPtr("-s"),
	}

	osClockConfigs := NewOSClockConfigs([]ptpv1.PtpProfile{*profile})
	realPhc2sys, err := NewPhc2sysProcess(ct.Env(0, profile, event.GM, &osClockConfigs))
	require.NoError(t, err)
	realChronyd, err := NewChronydProcess(ct.Env(0, profile, event.GM, &osClockConfigs))
	require.NoError(t, err)

	phc2sysStub := ct.AddProcess(realPhc2sys)
	chronydStub := ct.AddProcess(realChronyd)

	ct.StartPM()

	// Failover event triggers stop on phc2sys
	ct.SendPluginEvent(ntpfailover, generic.GnssRecoveredEventName)
	select {
	case <-ct.pm.eventsOut:
	case <-time.After(2 * time.Second):
		t.Fatal("failover event was not forwarded")
	}

	// Recovery event triggers start on phc2sys
	ct.SendPluginEvent(ntpfailover, generic.GnssRecoveredEventName)
	assert.Eventually(t, func() bool { return phc2sysStub.Starts() == 1 }, 2*time.Second, 10*time.Millisecond,
		"phc2sys must start when gnss_recovered event arrives")

	assert.NotNil(t, chronydStub.Conditions()[process.ActionStart])
	assert.NotNil(t, chronydStub.Conditions()[process.ActionStop])
}
