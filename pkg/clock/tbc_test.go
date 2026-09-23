package clock

import (
	"sync"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testControlledCfg = "ptp4l.1.config"
	testCfgName       = "ts2phc.0.config"
	testConfig        = "config"
	testIface         = "iface"
	testLeadingNIC    = "ens2f0"
	testEth0          = "eth0"
	testEth1          = "eth1"
	testEth2          = "eth2"
	testEth99         = "eth99"
	testIFace1        = "IFace1"
	testIface1Lower   = "iface1"
	testEno8703       = "eno8703"
	testEno8903       = "eno8903"
	testEns7f0        = "ens7f0"
	testPTP4lCfg      = "ptp4l.0.config"
	testTBCIface      = "ens1f0"
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

// eventRecorder captures events posted via TBC.sendEvent for assertions.
type eventRecorder struct {
	mu     sync.Mutex
	events []event.Event
}

func (r *eventRecorder) send(ev event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *eventRecorder) snapshot() []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]event.Event(nil), r.events...)
}

func newPMCTestTBCClock(pmcClient pmc.Client) *TBC {
	rec := &ipcRecorder{}
	return &TBC{
		sendIPC:          rec.send,
		sendEvent:        func(event.Event) {},
		getUtcOffset:     stubUtcOffset,
		pmcClient:        pmcClient,
		leadingClockData: newLeadingClockParams(),
		syncState: SyncState{
			State:         event.PTP_FREERUN,
			ClockClass:    protocol.ClockClassUninitialized,
			ClockAccuracy: fbprotocol.ClockAccuracyUnknown,
		},
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

// --- announceLocalData ---

func TestAnnounceLocalData_Freerun_SetsGMSettingsAndEGP(t *testing.T) {
	ensureLeapMocked(t)
	mock := &pmc.MockClient{}
	bc := newPMCTestTBCClock(mock)
	cfgName := testCfgName
	bc.leadingClockData.clockID = "001122.fffe.334455"
	bc.leadingClockData.controlledPortsConfig = testControlledCfg
	bc.syncState = SyncState{
		State:      event.PTP_FREERUN,
		ClockClass: protocol.ClockClassFreerun,
	}

	bc.announceLocalData(cfgName)

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
	bc := newPMCTestTBCClock(mock)
	cfgName := testCfgName
	bc.leadingClockData.clockID = "aabbcc.fffe.ddeeff"
	bc.leadingClockData.controlledPortsConfig = testControlledCfg
	bc.leadingClockData.downstreamTimeProperties = &protocol.TimePropertiesDS{
		CurrentUtcOffset:      37,
		CurrentUtcOffsetValid: true,
		Leap61:                true,
	}
	bc.syncState = SyncState{
		State:      event.PTP_HOLDOVER,
		ClockClass: fbprotocol.ClockClass(135),
	}

	bc.announceLocalData(cfgName)

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
	bc := newPMCTestTBCClock(mock)
	cfgName := testCfgName
	bc.leadingClockData.clockID = "aabbcc.fffe.ddeeff"
	bc.leadingClockData.controlledPortsConfig = testControlledCfg
	bc.leadingClockData.downstreamTimeProperties = &protocol.TimePropertiesDS{
		CurrentUtcOffset: 37,
	}
	bc.syncState = SyncState{
		State:      event.PTP_HOLDOVER,
		ClockClass: fbprotocol.ClockClass(165),
	}

	bc.announceLocalData(cfgName)

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
	bc := newPMCTestTBCClock(mock)
	cfgName := testCfgName

	bc.leadingClockData.clockID = "aabbcc.fffe.ddeeff"
	bc.leadingClockData.controlledPortsConfig = testControlledCfg
	bc.leadingClockData.downstreamTimeProperties = nil
	bc.syncState = SyncState{
		State:      event.PTP_HOLDOVER,
		ClockClass: fbprotocol.ClockClass(135),
	}

	bc.announceLocalData(cfgName)

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

// --- downstreamAnnounceIWF (fetch + emit only) ---

func TestDownstreamAnnounceIWF_Fetches_EmitsPMCEvent(t *testing.T) {
	cfgName := testCfgName
	want := pmc.ParentTimeCurrentDS{
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
		CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
	}
	mock := &pmc.MockClient{ParentTimeCurrentDSResult: want}

	bc := newPMCTestTBCClock(mock)
	rec := &eventRecorder{}
	bc.sendEvent = rec.send
	bc.syncState = SyncState{State: event.PTP_LOCKED}

	bc.announceToken = 7
	// runs on-loop in production (from a goroutine), here called synchronously.
	bc.downstreamAnnounceIWF(cfgName, bc.announceToken)

	getCalls := mock.SnapshotGetCalls()
	if !assert.Len(t, getCalls, 1) {
		return
	}
	assert.Equal(t, "GetParentTimeAndCurrentDS", getCalls[0].Method)
	assert.Equal(t, cfgName, getCalls[0].CfgName)

	// It must not touch PMC writes or clock state directly.
	assert.Empty(t, mock.SnapshotSetCalls())

	events := rec.snapshot()
	if !assert.Len(t, events, 1) {
		return
	}
	assert.Equal(t, event.PMC, events[0].Source)
	assert.Equal(t, cfgName, events[0].CfgName)
	payload, ok := events[0].Data.(*event.ParentTimeCurrentDS)
	if !assert.True(t, ok) {
		return
	}
	assert.Equal(t, want, payload.ParentTimeCurrentDS)
	// The captured epoch is stamped on the result so a stale fetch can be dropped.
	assert.Equal(t, uint64(7), payload.Generation)
}

func TestDownstreamAnnounceIWF_FetchError_NoEvent(t *testing.T) {
	mock := &pmc.MockClient{ParentTimeCurrentDSErr: assert.AnError}
	bc := newPMCTestTBCClock(mock)
	rec := &eventRecorder{}
	bc.sendEvent = rec.send
	bc.syncState = SyncState{State: event.PTP_LOCKED}

	bc.downstreamAnnounceIWF(testCfgName, bc.announceToken)

	assert.Len(t, mock.SnapshotGetCalls(), 1)
	assert.Empty(t, mock.SnapshotSetCalls())
	assert.Empty(t, rec.snapshot(), "no event should be emitted on fetch error")
}

// --- handleDownstreamAnnounceIWF (on-loop apply + write) ---

func TestHandleDownstreamAnnounceIWF_Locked_AppliesAndSets(t *testing.T) {
	mock := &pmc.MockClient{}
	bc := newPMCTestTBCClock(mock)
	bc.leadingClockData.controlledPortsConfig = testControlledCfg
	bc.syncState = SyncState{State: event.PTP_LOCKED}

	upstream := &event.ParentTimeCurrentDS{
		Generation: bc.announceToken,
		ParentTimeCurrentDS: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:              6,
				GrandmasterClockAccuracy:           0x21,
				GrandmasterOffsetScaledLogVariance: 0x4e5d,
				GrandmasterIdentity:                "aabb.ccdd.eeff",
			},
			TimePropertiesDS: protocol.TimePropertiesDS{PtpTimescale: true, TimeTraceable: true},
			CurrentDS:        protocol.CurrentDS{StepsRemoved: 1},
		},
	}

	bc.handleDownstreamAnnounceIWF(upstream)

	// State updates happen synchronously on the loop.
	assert.Equal(t, uint8(6), bc.leadingClockData.upstreamParentDataSet.GrandmasterClockClass)
	assert.Equal(t, uint8(6), bc.leadingClockData.downstreamParentDataSet.GrandmasterClockClass)
	assert.Equal(t, uint16(1), bc.leadingClockData.upstreamCurrentDSStepsRemoved)

	// PMC writes are dispatched off-loop.
	if !assert.Eventually(t, func() bool {
		return len(filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")) >= 1
	}, 1*time.Second, 10*time.Millisecond) {
		return
	}
	egpCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetExternalGMPropertiesNP")
	if assert.Len(t, egpCalls, 1) {
		assert.Equal(t, testControlledCfg, egpCalls[0].CfgName)
		assert.Equal(t, "aabb.ccdd.eeff", egpCalls[0].ExternalGMPropertiesNP.GrandmasterIdentity)
		assert.Equal(t, uint16(1), egpCalls[0].ExternalGMPropertiesNP.StepsRemoved)
	}
	gmCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")
	if assert.Len(t, gmCalls, 1) {
		assert.Equal(t, testControlledCfg, gmCalls[0].CfgName)
		assert.Equal(t, fbprotocol.ClockClass6, gmCalls[0].GMSettings.ClockQuality.ClockClass)
		assert.Equal(t, uint16(0x4e5d), gmCalls[0].GMSettings.ClockQuality.OffsetScaledLogVariance)
	}
}

func TestHandleDownstreamAnnounceIWF_NotLocked_Aborts(t *testing.T) {
	mock := &pmc.MockClient{}
	bc := newPMCTestTBCClock(mock)
	bc.syncState = SyncState{State: event.PTP_FREERUN}

	bc.handleDownstreamAnnounceIWF(&event.ParentTimeCurrentDS{
		Generation:          bc.announceToken,
		ParentTimeCurrentDS: pmc.ParentTimeCurrentDS{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6}},
	})

	// The aborted handle must not apply the upstream data to the baseline...
	assert.Equal(t, uint8(0), bc.leadingClockData.downstreamParentDataSet.GrandmasterClockClass)
	assert.Equal(t, uint8(0), bc.leadingClockData.upstreamParentDataSet.GrandmasterClockClass)
	// ...nor propagate anything to the controlled ports.
	assert.Empty(t, mock.SnapshotSetCalls())
}

func TestHandleDownstreamAnnounceIWF_StaleGeneration_Drops(t *testing.T) {
	mock := &pmc.MockClient{}
	bc := newPMCTestTBCClock(mock)
	bc.leadingClockData.controlledPortsConfig = testControlledCfg
	bc.syncState = SyncState{State: event.PTP_LOCKED}
	bc.announceToken = 5

	// A result carrying a previous epoch (e.g. fetched before a Reset/replace)
	// must be dropped even though the clock is currently LOCKED.
	bc.handleDownstreamAnnounceIWF(&event.ParentTimeCurrentDS{
		Generation:          4,
		ParentTimeCurrentDS: pmc.ParentTimeCurrentDS{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6}},
	})

	assert.Equal(t, uint8(0), bc.leadingClockData.upstreamParentDataSet.GrandmasterClockClass)
	assert.Equal(t, uint8(0), bc.leadingClockData.downstreamParentDataSet.GrandmasterClockClass)
	assert.Empty(t, mock.SnapshotSetCalls())
}

func TestReset_AdvancesGeneration(t *testing.T) {
	bc := newPMCTestTBCClock(&pmc.MockClient{})
	before := bc.announceToken
	bc.Reset()
	assert.NotEqual(t, before, bc.announceToken, "Reset must advance the epoch")
}

// TestHandleDownstreamAnnounceIWF_OutOfOrder_LateResultDoesNotClobber covers the
// core out-of-order hazard: a newer fetch result is applied, then an older fetch
// result (superseded but still in flight) lands late. The late result carries a
// stale token and must be dropped so it cannot overwrite the newer downstream
// data on the controlled ports.
func TestHandleDownstreamAnnounceIWF_OutOfOrder_LateResultDoesNotClobber(t *testing.T) {
	mock := &pmc.MockClient{}
	bc := newPMCTestTBCClock(mock)
	bc.leadingClockData.controlledPortsConfig = testControlledCfg
	bc.syncState = SyncState{State: event.PTP_LOCKED}

	// Two requests were issued; the second (token 11) supersedes the first
	// (token 10). The struct holds the latest token.
	stale := &event.ParentTimeCurrentDS{
		Generation:          10,
		ParentTimeCurrentDS: pmc.ParentTimeCurrentDS{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6}},
	}
	latest := &event.ParentTimeCurrentDS{
		Generation:          11,
		ParentTimeCurrentDS: pmc.ParentTimeCurrentDS{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 7}},
	}
	bc.announceToken = 11

	// Newer result lands first and is applied.
	bc.handleDownstreamAnnounceIWF(latest)
	assert.Equal(t, uint8(7), bc.leadingClockData.downstreamParentDataSet.GrandmasterClockClass)

	if !assert.Eventually(t, func() bool {
		return len(filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")) == 1
	}, 1*time.Second, 10*time.Millisecond) {
		return
	}

	// The superseded older result lands late; it must be thrown out.
	bc.handleDownstreamAnnounceIWF(stale)

	assert.Equal(t, uint8(7), bc.leadingClockData.downstreamParentDataSet.GrandmasterClockClass,
		"late stale result must not clobber the newer downstream data")
	assert.Equal(t, uint8(7), bc.leadingClockData.upstreamParentDataSet.GrandmasterClockClass)

	// No additional PMC writes were dispatched for the dropped result.
	assert.Len(t, filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings"), 1)
	assert.Len(t, filterSetCalls(mock.SnapshotSetCalls(), "SetExternalGMPropertiesNP"), 1)
	gmCalls := filterSetCalls(mock.SnapshotSetCalls(), "SetGMSettings")
	assert.Equal(t, fbprotocol.ClockClass(7), gmCalls[0].GMSettings.ClockQuality.ClockClass)
}

// TestUpdateDownstreamData_SupersededFetch_Dropped exercises the full round-trip:
// a second downstream request is issued while the first fetch is still "in
// flight" (its result captured but not yet handled). When the results are then
// delivered out of order via AddEvent, only the latest is applied.
func TestUpdateDownstreamData_SupersededFetch_Dropped(t *testing.T) {
	mock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6},
		},
	}
	bc := newPMCTestTBCClock(mock)
	bc.leadingClockData.controlledPortsConfig = testControlledCfg
	bc.syncState = SyncState{State: event.PTP_LOCKED}
	rec := &eventRecorder{}
	bc.sendEvent = rec.send

	// First request: fetch reads class 6 and emits an event carrying token T1.
	bc.updateDownstreamData(testCfgName)
	if !assert.Eventually(t, func() bool { return len(rec.snapshot()) >= 1 }, 1*time.Second, 10*time.Millisecond) {
		return
	}
	firstResult := rec.snapshot()[0]

	// Second request supersedes the first; make its fetch return class 7 so the
	// two results are distinguishable, and it carries a newer token T2.
	mock.ParentTimeCurrentDSResult = pmc.ParentTimeCurrentDS{
		ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 7},
	}
	bc.updateDownstreamData(testCfgName)
	if !assert.Eventually(t, func() bool { return len(rec.snapshot()) >= 2 }, 1*time.Second, 10*time.Millisecond) {
		return
	}
	secondResult := rec.snapshot()[1]

	firstPayload := firstResult.Data.(*event.ParentTimeCurrentDS)
	secondPayload := secondResult.Data.(*event.ParentTimeCurrentDS)
	assert.NotEqual(t, firstPayload.Generation, secondPayload.Generation, "each request must carry a distinct token")
	assert.Equal(t, bc.announceToken, secondPayload.Generation, "the struct tracks the latest request token")

	// Deliver out of order: the latest result first (applied)...
	bc.AddEvent(secondResult)
	assert.Equal(t, uint8(7), bc.leadingClockData.downstreamParentDataSet.GrandmasterClockClass)

	// ...then the superseded first result arrives late and must be dropped.
	bc.AddEvent(firstResult)
	assert.Equal(t, uint8(7), bc.leadingClockData.downstreamParentDataSet.GrandmasterClockClass,
		"stale first result must not overwrite the applied latest result")
}

// --- updateDownstreamData ---

func TestUpdateDownstreamData_Locked_FetchesAndEmitsEvent(t *testing.T) {
	mock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6},
			CurrentDS:     protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	bc := newPMCTestTBCClock(mock)
	rec := &eventRecorder{}
	bc.sendEvent = rec.send
	bc.leadingClockData.controlledPortsConfig = testControlledCfg
	bc.syncState = SyncState{State: event.PTP_LOCKED}

	bc.updateDownstreamData(testCfgName)

	// The LOCKED branch spawns downstreamAnnounceIWF as a goroutine: it fetches
	// and emits a PMC event; it does not perform PMC writes itself.
	if !assert.Eventually(t, func() bool {
		return len(rec.snapshot()) >= 1
	}, 1*time.Second, 10*time.Millisecond) {
		return
	}
	assert.Equal(t, "GetParentTimeAndCurrentDS", mock.SnapshotGetCalls()[0].Method)
	assert.Empty(t, mock.SnapshotSetCalls())
	assert.IsType(t, &event.ParentTimeCurrentDS{}, rec.snapshot()[0].Data)
}

func TestUpdateDownstreamData_Freerun_CallsAnnounceLocalData(t *testing.T) {
	ensureLeapMocked(t)
	mock := &pmc.MockClient{}
	bc := newPMCTestTBCClock(mock)
	cfgName := testCfgName
	bc.leadingClockData.clockID = "001122.fffe.334455"
	bc.leadingClockData.controlledPortsConfig = testControlledCfg
	bc.syncState = SyncState{
		State:      event.PTP_FREERUN,
		ClockClass: protocol.ClockClassFreerun,
	}

	bc.updateDownstreamData(cfgName)

	assert.Eventually(t, func() bool {
		return len(filterSetCalls(mock.SnapshotSetCalls(), "SetExternalGMPropertiesNP")) >= 1
	}, 1*time.Second, 10*time.Millisecond)
}

// --- TBC State Machine Tests ---

func TestUpdateLeadingClockData_PTP4lProcessName(t *testing.T) {
	ev := event.Event{
		Source: event.PTP4lProcessName,
		Data: &event.PTPData{
			Values: map[event.ValueType]interface{}{
				event.ControlledPortsConfig: testConfig,
				event.ClockIDKey:            "clockID",
			},
		},
	}

	bc := newPMCTestTBCClock(nil)
	bc.updateLeadingClockData(ev)

	assert.Equal(t, testConfig, bc.leadingClockData.controlledPortsConfig)
	assert.Equal(t, "clockID", bc.leadingClockData.clockID)
}

func TestUpdateLeadingClockData_DPLL(t *testing.T) {
	ev := event.Event{
		Source: event.DPLL,
		IFace:  testIface,
		Data: &event.PTPData{
			Values: map[event.ValueType]interface{}{
				event.LeadingSource:            true,
				event.InSyncConditionThreshold: uint64(100),
				event.InSyncConditionTimes:     uint64(200),
				event.ToFreeRunThreshold:       uint64(300),
				event.MaxInSpecOffset:          uint64(400),
			},
		},
	}

	bc := newPMCTestTBCClock(nil)
	bc.updateLeadingClockData(ev)

	assert.Equal(t, testIface, bc.leadingClockData.leadingInterface)
	assert.Equal(t, 100, bc.leadingClockData.inSyncConditionThreshold)
	assert.Equal(t, 200, bc.leadingClockData.inSyncConditionTimes)
	assert.Equal(t, 300, bc.leadingClockData.toFreeRunThreshold)
	assert.Equal(t, uint64(400), bc.leadingClockData.MaxInSpecOffset)
}

func TestGetLeadingInterfaceBC(t *testing.T) {
	tests := []struct {
		name     string
		lcp      *LeadingClockParams
		expected string
	}{
		{
			name:     "LeadingInterface is not empty",
			lcp:      &LeadingClockParams{leadingInterface: testEth0},
			expected: testEth0,
		},
		{
			name:     "LeadingInterface is empty",
			lcp:      &LeadingClockParams{},
			expected: event.LEADING_INTERFACE_UNKNOWN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bc := newPMCTestTBCClock(nil)
			bc.leadingClockData = tt.lcp
			result := bc.getLeadingInterfaceBC()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInSpecCondition(t *testing.T) {
	t.Run("returns false when MaxInSpecOffset is 0", func(t *testing.T) {
		bc := newPMCTestTBCClock(nil)
		bc.leadingClockData.MaxInSpecOffset = 0
		result := bc.inSpecCondition()
		assert.False(t, result)
	})

	t.Run("returns false when offset is out of spec", func(t *testing.T) {
		bc := newPMCTestTBCClock(nil)
		bc.leadingClockData.MaxInSpecOffset = 5
		bc.data = []*event.Data{
			{ProcessName: event.DPLL, Details: []*event.DataDetails{{IFace: testIface1Lower, Offset: 10}}},
		}
		bc.syncState.LeadingIFace = testIface1Lower
		result := bc.inSpecCondition()
		assert.False(t, result)
	})

	t.Run("returns true when offset is in spec", func(t *testing.T) {
		bc := newPMCTestTBCClock(nil)
		bc.leadingClockData.MaxInSpecOffset = 5
		bc.data = []*event.Data{
			{ProcessName: event.DPLL, Details: []*event.DataDetails{{IFace: testIface1Lower, Offset: 3}}},
		}
		bc.syncState.LeadingIFace = testIface1Lower
		result := bc.inSpecCondition()
		assert.True(t, result)
	})
}

func TestFreeRunCondition(t *testing.T) {
	tests := []struct {
		name         string
		data         []*event.Data
		LeadingIFace string
		threshold    int
		fillWindow   bool
		expected     bool
	}{
		{
			name:         "Free run condition not met",
			data:         []*event.Data{{ProcessName: event.DPLL, Details: []*event.DataDetails{{IFace: testIFace1, Offset: 5}}}},
			LeadingIFace: testIFace1,
			threshold:    10,
			expected:     false,
		},
		{
			name:         "Free run condition met",
			data:         []*event.Data{{ProcessName: event.DPLL, Details: []*event.DataDetails{{IFace: testIFace1, Offset: 15}}}},
			LeadingIFace: testIFace1,
			threshold:    10,
			expected:     true,
		},
		{
			name:         "Free run condition pending initialization",
			data:         nil,
			LeadingIFace: testIFace1,
			threshold:    0,
			expected:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fillWindow {
				for _, d := range tt.data {
					if d.ProcessName == event.PTP4l && len(d.Details) > 0 {
						d.Window = *utils.NewWindow(event.WindowSize)
						lastOffset := d.Details[len(d.Details)-1].Offset
						for i := 0; i < event.WindowSize; i++ {
							d.Window.Insert(float64(lastOffset))
						}
					}
				}
			}
			bc := newPMCTestTBCClock(nil)
			bc.leadingClockData.toFreeRunThreshold = tt.threshold
			bc.data = tt.data
			bc.syncState.LeadingIFace = tt.LeadingIFace

			result := bc.freeRunCondition()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetLargestOffset(t *testing.T) {
	currentTime := time.Now().Unix()
	recentTime := currentTime * 1000

	tests := []struct {
		name         string
		data         []*event.Data
		LeadingIFace string
		fillWindow   bool
		expected     int64
	}{
		{
			name:     "No data for config",
			data:     nil,
			expected: FaultyPhaseOffset,
		},
		{
			name:     "No process data",
			data:     []*event.Data{},
			expected: FaultyPhaseOffset,
		},
		{
			name: "Single offset value",
			data: []*event.Data{
				{ProcessName: event.DPLL, Details: []*event.DataDetails{
					{IFace: testEth0, Offset: 100, Time: recentTime},
				}},
			},
			LeadingIFace: testEth99,
			fillWindow:   true,
			expected:     100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fillWindow {
				for _, d := range tt.data {
					if len(d.Details) == 0 {
						continue
					}
					d.Window = *utils.NewWindow(event.WindowSize)
					for i := 0; i < event.WindowSize; i++ {
						offset := d.Details[i%len(d.Details)].Offset
						d.Window.Insert(float64(offset))
					}
				}
			}
			bc := newPMCTestTBCClock(nil)
			bc.data = tt.data
			bc.syncState.LeadingIFace = tt.LeadingIFace
			result := bc.getLargestOffset()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAddEvent_SourceLostPropagation(t *testing.T) {
	now := time.Now().UnixMilli()

	tests := []struct {
		name            string
		initialDetails  []*event.DataDetails
		event           event.Event
		expectedStates  map[string]event.PTPState
		expectedSrcLost map[string]bool
	}{
		{
			name: "source-lost does not propagate to other iface (no cross-hardware bleeding)",
			initialDetails: []*event.DataDetails{
				{IFace: testEno8903, State: event.PTP_LOCKED, SourceLost: false, Time: now - 5000},
				{IFace: testEno8703, State: event.PTP_LOCKED, SourceLost: false, Time: now - 3000},
			},
			event: event.Event{
				Source: event.PTP4l,
				IFace:  testEno8703,
				Time:   now,
				Data:   &event.PTPData{State: event.PTP_FREERUN, SourceLost: true},
			},
			expectedStates: map[string]event.PTPState{
				testEno8903: event.PTP_LOCKED,
				testEno8703: event.PTP_FREERUN,
			},
			expectedSrcLost: map[string]bool{
				testEno8903: false,
				testEno8703: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &event.Data{
				ProcessName: event.PTP4l,
				Details:     tt.initialDetails,
			}
			d.AddEvent(tt.event)
			for _, dd := range d.Details {
				assert.Equal(t, tt.expectedStates[dd.IFace], dd.State, "state for %s", dd.IFace)
				assert.Equal(t, tt.expectedSrcLost[dd.IFace], dd.SourceLost, "sourceLost for %s", dd.IFace)
			}
		})
	}
}

func TestIsSourceLostBC_StaleDetailFixed(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("stale LOCKED detail on other iface keeps isSourceLostBC false (no cross-hardware bleeding)", func(t *testing.T) {
		ptp4lData := &event.Data{
			ProcessName: event.PTP4l,
			Details: []*event.DataDetails{
				{IFace: testEno8903, State: event.PTP_LOCKED, SourceLost: false, Time: now - 5000},
				{IFace: testEno8703, State: event.PTP_LOCKED, SourceLost: false, Time: now - 3000},
			},
		}
		dpllData := &event.Data{
			ProcessName: event.DPLL,
			Details: []*event.DataDetails{
				{IFace: testEno8703, State: event.PTP_LOCKED, Time: now},
			},
		}

		bc := newPMCTestTBCClock(nil)
		bc.data = []*event.Data{ptp4lData, dpllData}

		assert.False(t, bc.isSourceLostBC(), "before source-lost event, ptpLost should be false")

		ptp4lData.AddEvent(event.Event{
			Source: event.PTP4l,
			IFace:  testEno8703,
			Time:   now,
			Data:   &event.PTPData{State: event.PTP_FREERUN, SourceLost: true},
		})

		assert.False(t, bc.isSourceLostBC(), "stale LOCKED detail on other iface prevents ptpLost")
	})
}

func TestUpdateBCState(t *testing.T) {
	t.Run("FREERUN to LOCKED", func(t *testing.T) {
		bc, _ := newLockedTBCClock()
		assert.Equal(t, event.PTP_LOCKED, bc.syncState.State, "should be LOCKED")
	})

	t.Run("LOCKED to FREERUN via offset", func(t *testing.T) {
		bc, _ := newLockedTBCClock()

		result := bc.AddEvent(makeTBCEvent(event.DPLL, event.PTP_LOCKED, 2000, false))
		assert.Equal(t, event.PTP_FREERUN, result.State, "should transition to FREERUN")
		assert.Equal(t, protocol.ClockClassFreerun, result.ClockClass, "clockClass should be 248")
	})

	t.Run("LOCKED to HOLDOVER via source lost", func(t *testing.T) {
		bc, _ := newLockedTBCClock()

		result := bc.AddEvent(makeTBCEvent(event.PTP4lProcessName, event.PTP_FREERUN, 10, true))
		assert.Equal(t, event.PTP_HOLDOVER, result.State, "should transition to HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(135), result.ClockClass, "clockClass should be 135 (holdover in-spec)")
	})

	t.Run("HOLDOVER to LOCKED", func(t *testing.T) {
		bc, _ := newLockedTBCClock()

		result := bc.AddEvent(makeTBCEvent(event.PTP4lProcessName, event.PTP_FREERUN, 10, true))
		assert.Equal(t, event.PTP_HOLDOVER, result.State, "setup: should be HOLDOVER")

		bc.leadingClockData.inSyncThresholdCounter = 0
		bc.AddEvent(makeTBCEvent(event.DPLL, event.PTP_LOCKED, 5, false))
		fillBCDataWindows(bc, 5)
		result = bc.AddEvent(makeTBCEvent(event.PTP4lProcessName, event.PTP_LOCKED, 5, false))
		assert.Equal(t, event.PTP_LOCKED, result.State, "should transition back to LOCKED")
	})

	t.Run("HOLDOVER to FREERUN via offset", func(t *testing.T) {
		bc, _ := newLockedTBCClock()

		result := bc.AddEvent(makeTBCEvent(event.PTP4lProcessName, event.PTP_FREERUN, 10, true))
		assert.Equal(t, event.PTP_HOLDOVER, result.State, "setup: should be HOLDOVER")

		result = bc.AddEvent(makeTBCEvent(event.DPLL, event.PTP_HOLDOVER, 2000, false))
		assert.Equal(t, event.PTP_FREERUN, result.State, "should transition to FREERUN")
		assert.Equal(t, protocol.ClockClassFreerun, result.ClockClass, "clockClass should be 248")
	})

	t.Run("HOLDOVER in-spec to out-of-spec", func(t *testing.T) {
		bc, _ := newLockedTBCClock()

		result := bc.AddEvent(makeTBCEvent(event.PTP4lProcessName, event.PTP_FREERUN, 10, true))
		assert.Equal(t, event.PTP_HOLDOVER, result.State, "setup: should be HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(135), result.ClockClass, "setup: should be in-spec (135)")

		fillBCDataWindows(bc, 600)
		result = bc.AddEvent(makeTBCEvent(event.DPLL, event.PTP_HOLDOVER, 600, false))
		assert.Equal(t, event.PTP_HOLDOVER, result.State, "should stay in HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(165), result.ClockClass, "clockClass should change to 165 (out-of-spec)")
	})
}

func TestProcessSyncE(t *testing.T) {
	t.Run("state event emits synce_state IPC", func(t *testing.T) {
		rio := &ipcRecorder{}
		bc := &TBC{cfgName: testPTP4lCfg, sendIPC: rio.send}
		ev := event.Event{
			Source: event.SYNCE,
			IFace:  testEns7f0,
			Data: &event.PTPData{
				Values: map[event.ValueType]interface{}{
					event.EEC_STATE: "EEC_LOCKED",
				},
			},
		}
		bc.processSyncE(ev)
		assert.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeSyncEState, rio.messages[0].Type)
		assert.Equal(t, testPTP4lCfg, rio.messages[0].Profile)
		assert.Equal(t, testEns7f0, rio.messages[0].IFace)
		assert.Equal(t, ipc.SyncEStateValue{State: "EEC_LOCKED"}, rio.messages[0].Values)
	})

	t.Run("quality event emits synce_clock_quality IPC", func(t *testing.T) {
		rio := &ipcRecorder{}
		bc := &TBC{cfgName: testPTP4lCfg, sendIPC: rio.send}
		ev := event.Event{
			Source: event.SYNCE,
			IFace:  testEns7f0,
			Data: &event.PTPData{
				Values: map[event.ValueType]interface{}{
					event.QL:     byte(4),
					event.EXT_QL: byte(0xFF),
				},
			},
		}
		bc.processSyncE(ev)
		assert.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeSyncEClockQuality, rio.messages[0].Type)
		assert.Equal(t, testPTP4lCfg, rio.messages[0].Profile)
		assert.Equal(t, ipc.SyncEClockQualityValue{QL: 4, ExtendedQL: 0xFF}, rio.messages[0].Values)
	})

	t.Run("nil PTPData does not panic", func(t *testing.T) {
		rio := &ipcRecorder{}
		bc := &TBC{cfgName: testPTP4lCfg, sendIPC: rio.send}
		bc.processSyncE(event.Event{Source: event.SYNCE, Data: nil})
		assert.Empty(t, rio.messages)
	})
}

func TestUpdateOSClockState(t *testing.T) {
	t.Run("changes when PTP and OS clock differ", func(t *testing.T) {
		rio := &ipcRecorder{}
		bc := newPMCTestTBCClock(nil)
		bc.sendIPC = rio.send
		bc.syncState.State = event.PTP_LOCKED
		bc.overallSyncState = event.PTP_LOCKED

		bc.SystemClockUpdate(event.PTP_FREERUN)
		assert.Equal(t, event.PTP_FREERUN, bc.overallSyncState)
		assert.Equal(t, event.PTP_FREERUN, bc.osClockState)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeSyncState, rio.messages[0].Type)
		assert.Equal(t, ipc.SyncStateValue{State: ipc.StateFreerun}, rio.messages[0].Values)
	})

	t.Run("no change when already correct", func(t *testing.T) {
		rio := &ipcRecorder{}
		bc := newPMCTestTBCClock(nil)
		bc.sendIPC = rio.send
		bc.syncState.State = event.PTP_LOCKED
		bc.overallSyncState = event.PTP_LOCKED

		bc.SystemClockUpdate(event.PTP_LOCKED)
		assert.Equal(t, event.PTP_LOCKED, bc.overallSyncState)
		assert.Empty(t, rio.messages)
	})
}

func TestTBCClock_ParentDSUpdate(t *testing.T) {
	t.Run("stores upstream parent dataset", func(t *testing.T) {
		bc := newPMCTestTBCClock(nil)
		parentDS := protocol.ParentDataSet{
			GrandmasterIdentity:   "001122.fffe.334455",
			GrandmasterClockClass: 6,
		}

		bc.AddEvent(event.Event{Source: event.PMC, Data: &event.ParentDSData{ParentDataSet: parentDS}})

		assert.True(t, parentDS.Equal(bc.leadingClockData.upstreamParentDataSet))
	})

	t.Run("LOCKED clock propagates upstream clock class immediately", func(t *testing.T) {
		bc, _ := newLockedTBCClock()
		assert.Equal(t, event.PTP_LOCKED, bc.syncState.State, "precondition: clock must be LOCKED")

		bc.AddEvent(event.Event{Source: event.PMC, Data: &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{
			GrandmasterClockClass: 6,
		}}})

		assert.Equal(t, fbprotocol.ClockClass(6), bc.syncState.ClockClass,
			"clock class should update to upstream value")
		assert.True(t, bc.leadingClockData.upstreamParentDataSet.Equal(
			bc.leadingClockData.downstreamParentDataSet),
			"downstream ParentDataSet should match upstream after propagation")
	})

	t.Run("LOCKED clock sends IPC on clock class change", func(t *testing.T) {
		bc, rio := newLockedTBCClock()
		bc.AddEvent(event.Event{Source: event.PMC, Data: &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{
			GrandmasterClockClass: 7,
		}}})

		var foundClockClassIPC bool
		for _, msg := range rio.messages {
			if msg.Type == ipc.TypeClockClass {
				foundClockClassIPC = true
				assert.Equal(t, ipc.ClockClassValue{ClockClass: 7}, msg.Values)
			}
		}
		assert.True(t, foundClockClassIPC, "should send clock class IPC message")
	})
}

// Helper functions for TBC tests
func fillBCDataWindows(bc *TBC, offset int64) {
	for _, d := range bc.data {
		d.Window = *utils.NewWindow(event.WindowSize)
		for i := 0; i < event.WindowSize; i++ {
			d.Window.Insert(float64(offset))
		}
	}
}

func makeTBCEvent(process event.EventSource, state event.PTPState, offset int64, sourceLost bool) event.Event {
	return event.Event{
		Source:     process,
		IFace:      testTBCIface,
		CfgName:    testPTP4lCfg,
		ClockType:  event.BC,
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data: &event.PTPData{
			State:      state,
			Values:     map[event.ValueType]interface{}{event.OFFSET: offset},
			SourceLost: sourceLost,
		},
	}
}

func tbcLeadingClockParams() *LeadingClockParams {
	return &LeadingClockParams{
		leadingInterface:         testTBCIface,
		inSyncConditionThreshold: 100,
		inSyncConditionTimes:     1,
		toFreeRunThreshold:       1500,
		MaxInSpecOffset:          500,
		upstreamParentDataSet:    &protocol.ParentDataSet{},
		upstreamTimeProperties:   &protocol.TimePropertiesDS{},
		downstreamParentDataSet:  &protocol.ParentDataSet{},
		downstreamTimeProperties: &protocol.TimePropertiesDS{},
	}
}

func newLockedTBCClock() (*TBC, *ipcRecorder) {
	rec := ipcRecorder{}
	bc := &TBC{
		sendIPC:          rec.send,
		sendEvent:        func(event.Event) {},
		getUtcOffset:     stubUtcOffset,
		pmcClient:        &pmc.MockClient{},
		cfgName:          testTS2PHCCfg,
		leadingClockData: tbcLeadingClockParams(),
		syncState: SyncState{
			State:         event.PTP_FREERUN,
			ClockClass:    protocol.ClockClassUninitialized,
			ClockAccuracy: fbprotocol.ClockAccuracyUnknown,
		},
		overallSyncState: event.PTP_FREERUN,
		osClockState:     event.PTP_NOTSET,
	}
	bc.AddEvent(makeTBCEvent(event.DPLL, event.PTP_LOCKED, 10, false))
	bc.AddEvent(makeTBCEvent(event.PTP4lProcessName, event.PTP_LOCKED, 10, false))
	fillBCDataWindows(bc, 10)
	bc.AddEvent(makeTBCEvent(event.DPLL, event.PTP_LOCKED, 10, false))
	return bc, &rec
}
