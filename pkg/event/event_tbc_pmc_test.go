package event

import (
	"context"
	"sync"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
)

const (
	testControlledCfg = "ptp4l.1.config"
	testCfgName       = "ts2phc.0.config"
)

var leapOnce sync.Once

func ensureLeapMocked(t *testing.T) {
	t.Helper()
	leapOnce.Do(func() {
		if err := leap.MockLeapFile(); err != nil {
			t.Fatalf("failed to mock leap file: %v", err)
		}
	})
}

func newPMCTestEventHandler() *EventHandler {
	if StateRegisterer == nil {
		StateRegisterer = NewStateNotifier()
	}
	return &EventHandler{
		data:             map[string][]*Data{},
		clkSyncState:     map[string]*clockSyncState{},
		downstreamCancel: map[string]context.CancelFunc{},
		LeadingClockData: newLeadingClockParams(),
	}
}

func filterSetCalls(calls []pmc.SetCall, method string) []pmc.SetCall {
	var out []pmc.SetCall
	for _, c := range calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// --- applyIfLockedBC ---

func TestApplyIfLockedBC_LockedRunsFn(t *testing.T) {
	e := newPMCTestEventHandler()
	cfgName := testCfgName
	e.clkSyncState[cfgName] = &clockSyncState{state: PTP_LOCKED}

	called := false
	ok := e.applyIfLockedBC(cfgName, "test", func() { called = true })

	assert.True(t, ok)
	assert.True(t, called)
}

func TestApplyIfLockedBC_NotLockedSkipsFn(t *testing.T) {
	e := newPMCTestEventHandler()
	cfgName := testCfgName
	e.clkSyncState[cfgName] = &clockSyncState{state: PTP_FREERUN}

	called := false
	ok := e.applyIfLockedBC(cfgName, "test", func() { called = true })

	assert.False(t, ok)
	assert.False(t, called)
}

func TestApplyIfLockedBC_MissingConfigSkipsFn(t *testing.T) {
	e := newPMCTestEventHandler()

	called := false
	ok := e.applyIfLockedBC("nonexistent", "test", func() { called = true })

	assert.False(t, ok)
	assert.False(t, called)
}

// --- announceLocalData ---

func TestAnnounceLocalData_Freerun_SetsGMSettingsAndEGP(t *testing.T) {
	ensureLeapMocked(t)
	mock := &pmc.MockClient{}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()
	cfgName := testCfgName
	e.LeadingClockData.clockID = "001122.fffe.334455"
	e.LeadingClockData.controlledPortsConfig = testControlledCfg
	e.clkSyncState[cfgName] = &clockSyncState{
		state:      PTP_FREERUN,
		clockClass: protocol.ClockClassFreerun,
	}

	e.announceLocalData(cfgName)

	// 3 async goroutines: 1 EGP + 2 GM settings
	if !assert.Eventually(t, func() bool {
		return len(mock.SnapshotSetCalls()) >= 3
	}, 1*time.Second, 10*time.Millisecond) {
		return
	}

	egpCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetExternalGMPropertiesNP")
	if !assert.Len(t, egpCalls, 1) {
		return
	}
	assert.Equal(t, testControlledCfg, egpCalls[0].CfgName)
	assert.Equal(t, "001122.fffe.334455", egpCalls[0].ExternalGMPropertiesNP.GrandmasterIdentity)
	assert.Equal(t, uint16(0), egpCalls[0].ExternalGMPropertiesNP.StepsRemoved)

	gmCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")
	if !assert.Len(t, gmCalls, 2) {
		return
	}

	configs := []string{gmCalls[0].CfgName, gmCalls[1].CfgName}
	assert.Contains(t, configs, testControlledCfg)
	assert.Contains(t, configs, cfgName)

	for _, call := range gmCalls {
		gs := call.GMSettings
		if !assert.NotNil(t, gs) {
			continue
		}
		assert.Equal(t, protocol.ClockClassFreerun, gs.ClockQuality.ClockClass)
		assert.Equal(t, fbprotocol.ClockAccuracyUnknown, gs.ClockQuality.ClockAccuracy)
		assert.True(t, gs.TimePropertiesDS.PtpTimescale)
		assert.False(t, gs.TimePropertiesDS.TimeTraceable)
		assert.False(t, gs.TimePropertiesDS.FrequencyTraceable)
	}
}

func TestAnnounceLocalData_Holdover135_SetsTimeProperties(t *testing.T) {
	mock := &pmc.MockClient{}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()
	cfgName := testCfgName
	e.LeadingClockData.clockID = "aabbcc.fffe.ddeeff"
	e.LeadingClockData.controlledPortsConfig = testControlledCfg
	e.LeadingClockData.downstreamTimeProperties = &protocol.TimePropertiesDS{
		CurrentUtcOffset:      37,
		CurrentUtcOffsetValid: true,
		Leap61:                true,
	}
	e.clkSyncState[cfgName] = &clockSyncState{
		state:      PTP_HOLDOVER,
		clockClass: fbprotocol.ClockClass(135),
	}

	e.announceLocalData(cfgName)

	if !assert.Eventually(t, func() bool {
		return len(filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")) >= 2
	}, 1*time.Second, 10*time.Millisecond) {
		return
	}

	gmCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")
	if !assert.Len(t, gmCalls, 2) {
		return
	}

	for _, call := range gmCalls {
		gs := call.GMSettings
		if !assert.NotNil(t, gs) {
			continue
		}
		assert.Equal(t, fbprotocol.ClockClass(135), gs.ClockQuality.ClockClass)
		assert.True(t, gs.TimePropertiesDS.PtpTimescale)
		assert.True(t, gs.TimePropertiesDS.TimeTraceable, "class 135 should be time traceable")
		assert.True(t, gs.TimePropertiesDS.CurrentUtcOffsetValid)
		assert.Equal(t, int32(37), gs.TimePropertiesDS.CurrentUtcOffset)
		assert.True(t, gs.TimePropertiesDS.Leap61)
	}
}

func TestAnnounceLocalData_Holdover165_NotTimeTraceable(t *testing.T) {
	mock := &pmc.MockClient{}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()
	cfgName := testCfgName
	e.LeadingClockData.clockID = "aabbcc.fffe.ddeeff"
	e.LeadingClockData.controlledPortsConfig = testControlledCfg
	e.LeadingClockData.downstreamTimeProperties = &protocol.TimePropertiesDS{
		CurrentUtcOffset: 37,
	}
	e.clkSyncState[cfgName] = &clockSyncState{
		state:      PTP_HOLDOVER,
		clockClass: fbprotocol.ClockClass(165),
	}

	e.announceLocalData(cfgName)

	if !assert.Eventually(t, func() bool {
		return len(filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")) >= 2
	}, 1*time.Second, 10*time.Millisecond) {
		return
	}

	gmCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")
	if !assert.Len(t, gmCalls, 2) {
		return
	}

	for _, call := range gmCalls {
		gs := call.GMSettings
		if !assert.NotNil(t, gs) {
			continue
		}
		assert.Equal(t, fbprotocol.ClockClass(165), gs.ClockQuality.ClockClass)
		assert.False(t, gs.TimePropertiesDS.TimeTraceable, "class 165 should NOT be time traceable")
	}
}

func TestAnnounceLocalData_NilDownstreamTimeProperties_SkipsGMSettings(t *testing.T) {
	mock := &pmc.MockClient{}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()
	cfgName := testCfgName

	e.LeadingClockData.clockID = "aabbcc.fffe.ddeeff"
	e.LeadingClockData.controlledPortsConfig = testControlledCfg
	e.LeadingClockData.downstreamTimeProperties = nil
	e.clkSyncState[cfgName] = &clockSyncState{
		state:      PTP_HOLDOVER,
		clockClass: fbprotocol.ClockClass(135),
	}

	e.announceLocalData(cfgName)

	// EGP runs in a goroutine; GM settings should be skipped
	if !assert.Eventually(t, func() bool {
		return len(filterSetCalls(mock.SnapshotSetCalls(), "SetExternalGMPropertiesNP")) >= 1
	}, 1*time.Second, 10*time.Millisecond) {
		return
	}

	egpCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetExternalGMPropertiesNP")
	assert.Len(t, egpCalls, 1)

	gmCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")
	assert.Empty(t, gmCalls)
}

func TestAnnounceLocalData_MissingClkSyncState_Returns(t *testing.T) {
	mock := &pmc.MockClient{}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()

	e.announceLocalData("nonexistent")

	// Returns synchronously before launching any goroutines
	assert.Empty(t, mock.SnapshotSetCalls())
	assert.Empty(t, mock.SnapshotGetCalls())
}

// --- downstreamAnnounceIWF ---

func TestDownstreamAnnounceIWF_Locked_FetchesAndSetsDownstream(t *testing.T) {
	cfgName := testCfgName
	mock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:              6,
				GrandmasterClockAccuracy:           0x21,
				GrandmasterOffsetScaledLogVariance: 0x4e5d,
				GrandmasterIdentity:                "aabb.ccdd.eeff",
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{
				StepsRemoved: 1,
			},
		},
	}
	pmc.SetMock(mock)
	defer pmc.ResetMock()

	e := newPMCTestEventHandler()
	e.LeadingClockData.controlledPortsConfig = testControlledCfg
	e.clkSyncState[cfgName] = &clockSyncState{state: PTP_LOCKED}

	ctx := context.Background()
	e.downstreamAnnounceIWF(ctx, cfgName)

	getCalls := mock.SnapshotGetCalls()
	if !assert.Len(t, getCalls, 1) {
		return
	}
	assert.Equal(t, "GetParentTimeAndCurrentDS", getCalls[0].Method)
	assert.Equal(t, cfgName, getCalls[0].CfgName)

	egpCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetExternalGMPropertiesNP")
	if !assert.Len(t, egpCalls, 1) {
		return
	}
	assert.Equal(t, testControlledCfg, egpCalls[0].CfgName)
	assert.Equal(t, "aabb.ccdd.eeff", egpCalls[0].ExternalGMPropertiesNP.GrandmasterIdentity)
	assert.Equal(t, uint16(1), egpCalls[0].ExternalGMPropertiesNP.StepsRemoved)

	gmCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")
	if !assert.Len(t, gmCalls, 1) {
		return
	}
	assert.Equal(t, testControlledCfg, gmCalls[0].CfgName)
	gs := gmCalls[0].GMSettings
	if !assert.NotNil(t, gs) {
		return
	}
	assert.Equal(t, fbprotocol.ClockClass6, gs.ClockQuality.ClockClass)
	assert.Equal(t, fbprotocol.ClockAccuracy(0x21), gs.ClockQuality.ClockAccuracy)
	assert.Equal(t, uint16(0x4e5d), gs.ClockQuality.OffsetScaledLogVariance)
	assert.True(t, gs.TimePropertiesDS.PtpTimescale)
	assert.True(t, gs.TimePropertiesDS.TimeTraceable)

	assert.Equal(t, uint8(6), e.LeadingClockData.upstreamParentDataSet.GrandmasterClockClass)
	assert.Equal(t, uint8(6), e.LeadingClockData.downstreamParentDataSet.GrandmasterClockClass)
	assert.Equal(t, uint16(1), e.LeadingClockData.upstreamCurrentDSStepsRemoved)
}

func TestDownstreamAnnounceIWF_FetchError_ReturnsEarly(t *testing.T) {
	mock := &pmc.MockClient{
		ParentTimeCurrentDSErr: assert.AnError,
	}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()
	cfgName := testCfgName
	e.clkSyncState[cfgName] = &clockSyncState{state: PTP_LOCKED}

	ctx := context.Background()
	e.downstreamAnnounceIWF(ctx, cfgName)

	getCalls := mock.SnapshotGetCalls()
	if !assert.Len(t, getCalls, 1) {
		return
	}
	assert.Empty(t, mock.SnapshotSetCalls())
}

func TestDownstreamAnnounceIWF_NotLocked_AbortsAfterFetch(t *testing.T) {
	mock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6},
		},
	}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()
	cfgName := testCfgName
	e.clkSyncState[cfgName] = &clockSyncState{state: PTP_FREERUN}

	ctx := context.Background()
	e.downstreamAnnounceIWF(ctx, cfgName)

	getCalls := mock.SnapshotGetCalls()
	if !assert.Len(t, getCalls, 1) {
		return
	}
	assert.Empty(t, mock.SnapshotSetCalls())
}

func TestDownstreamAnnounceIWF_CancelledBeforeAnnounce(t *testing.T) {
	mock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6},
		},
	}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()
	cfgName := testCfgName
	e.clkSyncState[cfgName] = &clockSyncState{state: PTP_LOCKED}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	e.downstreamAnnounceIWF(ctx, cfgName)

	getCalls := mock.SnapshotGetCalls()
	if !assert.Len(t, getCalls, 1) {
		return
	}
	assert.Empty(t, mock.SnapshotSetCalls())
}

// --- updateDownstreamData ---

func TestUpdateDownstreamData_Locked_CallsDownstreamAnnounceIWF(t *testing.T) {
	mock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6},
			CurrentDS:     protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()
	cfgName := testCfgName
	e.LeadingClockData.controlledPortsConfig = testControlledCfg
	e.clkSyncState[cfgName] = &clockSyncState{state: PTP_LOCKED}

	e.updateDownstreamData(cfgName)

	if !assert.Eventually(t, func() bool {
		return len(mock.SnapshotGetCalls()) >= 1
	}, 1*time.Second, 10*time.Millisecond) {
		return
	}

	getCalls := mock.SnapshotGetCalls()
	assert.Equal(t, "GetParentTimeAndCurrentDS", getCalls[0].Method)
}

func TestUpdateDownstreamData_Freerun_CallsAnnounceLocalData(t *testing.T) {
	ensureLeapMocked(t)
	mock := &pmc.MockClient{}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()
	cfgName := testCfgName
	e.LeadingClockData.clockID = "001122.fffe.334455"
	e.LeadingClockData.controlledPortsConfig = testControlledCfg
	e.clkSyncState[cfgName] = &clockSyncState{
		state:      PTP_FREERUN,
		clockClass: protocol.ClockClassFreerun,
	}

	e.updateDownstreamData(cfgName)

	assert.Eventually(t, func() bool {
		return len(filterSetCalls(mock.SnapshotSetCalls(), "SetExternalGMPropertiesNP")) >= 1
	}, 1*time.Second, 10*time.Millisecond)
}

func TestUpdateDownstreamData_MissingConfig_Returns(t *testing.T) {
	mock := &pmc.MockClient{}
	pmc.SetMock(mock)
	defer pmc.ResetMock()
	e := newPMCTestEventHandler()

	e.updateDownstreamData("nonexistent")

	// Returns synchronously before launching any goroutines
	assert.Empty(t, mock.SnapshotSetCalls())
	assert.Empty(t, mock.SnapshotGetCalls())
}
