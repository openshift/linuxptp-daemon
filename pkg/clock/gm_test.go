package clock

import (
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testTS2PHCCfg = "ts2phc.0.config"
)

func newTestGMClock() (*GM, *ipcRecorder, *pmc.MockClient) {
	rio := &ipcRecorder{}
	pmcMock := &pmc.MockClient{}
	gm := &GM{
		cfgName:      "ts2phc.0.config",
		sendIPC:      rio.send,
		getUtcOffset: stubUtcOffset,
		pmcClient:    pmcMock,
		syncState: SyncState{
			State:         event.PTP_NOTSET,
			ClockClass:    protocol.ClockClassUninitialized,
			ClockAccuracy: fbprotocol.ClockAccuracyUnknown,
		},
		overallSyncState: event.PTP_NOTSET,
		osClockState:     event.PTP_NOTSET,
		gnssState:        event.PTP_NOTSET,
	}
	return gm, rio, pmcMock
}

func gmData(process event.EventSource, state event.PTPState) *event.Data {
	d := &event.Data{
		ProcessName: process,
		State:       state,
		Details: []*event.DataDetails{{
			IFace: testTBCIface,
			State: state,
		}},
	}
	if process == event.TS2PHCProcessName {
		d.Details[0].SignalSource = event.GNSS
	}
	return d
}

func TestGMClock_UpdateState_IPCEmission(t *testing.T) {
	const cfg = "ts2phc.0.config"
	const iface = "ens1f0"

	t.Run("FREERUN to LOCKED emits TypePTPState", func(t *testing.T) {
		gm, rio, _ := newTestGMClock()

		// First call: establish FREERUN (from PTP_NOTSET, no IPC emitted)
		gm.data = []*event.Data{
			gmData(event.GNSS, event.PTP_LOCKED),
			gmData(event.DPLL, event.PTP_LOCKED),
			gmData(event.TS2PHCProcessName, event.PTP_FREERUN),
		}
		result := gm.updateState()
		assert.Equal(t, event.PTP_FREERUN, result.State)

		var gotGNSS bool
		for _, msg := range rio.messages {
			assert.NotEqual(t, ipc.TypePTPState, msg.Type, "no TypePTPState IPC from PTP_NOTSET→FREERUN")
			if msg.Type == ipc.TypeGNSSState {
				gv := msg.Values.(ipc.GNSSStateValue)
				assert.Equal(t, ipc.StateLocked, gv.State)
				assert.Equal(t, cfg, msg.Profile)
				assert.Equal(t, iface, msg.IFace)
				gotGNSS = true
			}
		}
		assert.True(t, gotGNSS, "expected TypeGNSSState LOCKED on initial GNSS transition")
		rio.messages = nil

		// Second call: transition to LOCKED
		gm.data[2] = gmData(event.TS2PHCProcessName, event.PTP_LOCKED)
		result = gm.updateState()
		assert.Equal(t, event.PTP_LOCKED, result.State)
		assert.Equal(t, fbprotocol.ClockClass6, result.ClockClass)

		var gotState, gotClass6 bool
		for _, msg := range rio.messages {
			if msg.Type == ipc.TypePTPState {
				sv := msg.Values.(ipc.StateValue)
				assert.Equal(t, ipc.StateLocked, sv.State)
				assert.Equal(t, cfg, msg.Profile)
				gotState = true
			}
			if msg.Type == ipc.TypeClockClass {
				cv := msg.Values.(ipc.ClockClassValue)
				assert.Equal(t, uint8(6), cv.ClockClass)
				gotClass6 = true
			}
			assert.NotEqual(t, ipc.TypeGNSSState, msg.Type, "no GNSS IPC when GNSS state unchanged")
		}
		assert.True(t, gotState, "expected TypePTPState LOCKED")
		assert.True(t, gotClass6, "expected TypeClockClass 6 on FREERUN→LOCKED")
	})

	t.Run("LOCKED to HOLDOVER emits TypePTPState and TypeClockClass", func(t *testing.T) {
		gm, rio, _ := newTestGMClock()

		// Establish FREERUN then LOCKED
		gm.data = []*event.Data{
			gmData(event.GNSS, event.PTP_LOCKED),
			gmData(event.DPLL, event.PTP_LOCKED),
			gmData(event.TS2PHCProcessName, event.PTP_FREERUN),
		}
		gm.updateState()
		gm.data[2] = gmData(event.TS2PHCProcessName, event.PTP_LOCKED)
		gm.updateState()
		rio.messages = nil // clear

		// DPLL holdover
		gm.data[1] = gmData(event.DPLL, event.PTP_HOLDOVER)
		result := gm.updateState()
		assert.Equal(t, event.PTP_HOLDOVER, result.State)
		assert.Equal(t, fbprotocol.ClockClass7, result.ClockClass)

		var gotState, gotClass bool
		for _, msg := range rio.messages {
			if msg.Type == ipc.TypePTPState {
				sv := msg.Values.(ipc.StateValue)
				assert.Equal(t, ipc.StateHoldover, sv.State)
				gotState = true
			}
			if msg.Type == ipc.TypeClockClass {
				cv := msg.Values.(ipc.ClockClassValue)
				assert.Equal(t, uint8(7), cv.ClockClass)
				gotClass = true
			}
			assert.NotEqual(t, ipc.TypeGNSSState, msg.Type, "no GNSS IPC when GNSS state unchanged")
		}
		assert.True(t, gotState, "expected TypePTPState HOLDOVER")
		assert.True(t, gotClass, "expected TypeClockClass 7")
	})

	t.Run("duplicate state produces no IPC", func(t *testing.T) {
		gm, rio, _ := newTestGMClock()

		gm.data = []*event.Data{
			gmData(event.GNSS, event.PTP_LOCKED),
			gmData(event.DPLL, event.PTP_LOCKED),
			gmData(event.TS2PHCProcessName, event.PTP_FREERUN),
		}
		gm.updateState()
		gm.updateState()
		rio.messages = nil

		// Same state again
		gm.updateState()
		assert.Empty(t, rio.messages, "duplicate events should not emit IPC")
	})

	t.Run("GNSS LOCKED to FREERUN emits TypeGNSSState", func(t *testing.T) {
		gm, rio, _ := newTestGMClock()

		// Establish GNSS LOCKED
		gm.data = []*event.Data{
			gmData(event.GNSS, event.PTP_LOCKED),
			gmData(event.DPLL, event.PTP_LOCKED),
			gmData(event.TS2PHCProcessName, event.PTP_FREERUN),
		}
		gm.updateState()
		rio.messages = nil

		// GNSS drops to FREERUN
		gm.data[0] = gmData(event.GNSS, event.PTP_FREERUN)
		gm.updateState()

		var gotGNSS bool
		for _, msg := range rio.messages {
			if msg.Type == ipc.TypeGNSSState {
				gv := msg.Values.(ipc.GNSSStateValue)
				assert.Equal(t, ipc.StateFreerun, gv.State)
				assert.Equal(t, cfg, msg.Profile)
				assert.Equal(t, iface, msg.IFace)
				gotGNSS = true
			}
		}
		assert.True(t, gotGNSS, "expected TypeGNSSState FREERUN when GNSS drops")
	})

	t.Run("LOCKED to FREERUN emits TypeClockClass 248", func(t *testing.T) {
		gm, rio, _ := newTestGMClock()

		// Establish LOCKED
		gm.data = []*event.Data{
			gmData(event.GNSS, event.PTP_LOCKED),
			gmData(event.DPLL, event.PTP_LOCKED),
			gmData(event.TS2PHCProcessName, event.PTP_FREERUN),
		}
		gm.updateState()
		gm.data[2] = gmData(event.TS2PHCProcessName, event.PTP_LOCKED)
		gm.updateState()
		rio.messages = nil

		// ts2phc FREERUN → GM FREERUN
		gm.data[2] = gmData(event.TS2PHCProcessName, event.PTP_FREERUN)
		result := gm.updateState()
		assert.Equal(t, event.PTP_FREERUN, result.State)
		assert.Equal(t, protocol.ClockClassFreerun, result.ClockClass)

		var gotClass bool
		for _, msg := range rio.messages {
			if msg.Type == ipc.TypeClockClass {
				cv := msg.Values.(ipc.ClockClassValue)
				assert.Equal(t, uint8(protocol.ClockClassFreerun), cv.ClockClass)
				gotClass = true
			}
		}
		assert.True(t, gotClass, "expected TypeClockClass 248")
	})
}

func TestGMClock_UpdateOSClockState(t *testing.T) {
	gm, rio, _ := newTestGMClock()
	gm.syncState.State = event.PTP_LOCKED

	gm.SystemClockUpdate(event.PTP_LOCKED)
	assert.Equal(t, event.PTP_LOCKED, gm.overallSyncState, "first call from PTP_NOTSET should change")
	assert.Equal(t, event.PTP_LOCKED, gm.osClockState)
	require.Len(t, rio.messages, 1)
	assert.Equal(t, ipc.TypeSyncState, rio.messages[0].Type)

	rio.messages = nil
	gm.SystemClockUpdate(event.PTP_LOCKED)
	assert.Empty(t, rio.messages, "same state should not emit IPC")

	gm.SystemClockUpdate(event.PTP_FREERUN)
	assert.Equal(t, event.PTP_FREERUN, gm.overallSyncState, "OS clock FREERUN should degrade overall")
	require.Len(t, rio.messages, 1)
	assert.Equal(t, ipc.TypeSyncState, rio.messages[0].Type)
	assert.Equal(t, ipc.SyncStateValue{State: ipc.StateFreerun}, rio.messages[0].Values)
}

func TestGMClock_UpdateClockClass(t *testing.T) {
	gm, rio, _ := newTestGMClock()
	gm.syncState.ClockClass = fbprotocol.ClockClass6
	gm.syncState.LeadingIFace = "ens1f0"

	// Same class → no IPC
	gm.updateClockClass(fbprotocol.ClockClass6)
	assert.Empty(t, rio.messages)

	// Different class → IPC emitted
	gm.updateClockClass(fbprotocol.ClockClass7)
	assert.Len(t, rio.messages, 1)
	assert.Equal(t, ipc.TypeClockClass, rio.messages[0].Type)
	cv := rio.messages[0].Values.(ipc.ClockClassValue)
	assert.Equal(t, uint8(7), cv.ClockClass)
}

func TestGMClock_AnnounceClockClassIfChanged(t *testing.T) {
	t.Run("skips when clockClass is uninitialized", func(t *testing.T) {
		gm, _, pmcMock := newTestGMClock()
		gm.announceClockClassIfChanged(
			event.Event{Source: event.DPLL, Data: &event.PTPData{Values: map[event.ValueType]interface{}{event.OFFSET: int64(50)}}},
			SyncState{State: event.PTP_LOCKED, ClockClass: protocol.ClockClassUninitialized},
		)
		assert.Empty(t, pmcMock.SnapshotSetCalls())
	})

	t.Run("sets GM settings on class change", func(t *testing.T) {
		gm, _, pmcMock := newTestGMClock()
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.ClockClass = protocol.ClockClassFreerun
		gm.announceClockClassIfChanged(
			event.Event{Source: event.GNSS, Data: &event.GNSSData{}},
			SyncState{State: event.PTP_FREERUN, ClockClass: protocol.ClockClassFreerun},
		)
		setCalls := pmcMock.SnapshotSetCalls()
		require.Len(t, setCalls, 1)
		assert.Equal(t, protocol.ClockClassFreerun, setCalls[0].GMSettings.ClockQuality.ClockClass)
		assert.Equal(t, fbprotocol.ClockAccuracyUnknown, setCalls[0].GMSettings.ClockQuality.ClockAccuracy)
	})

	t.Run("no PMC call when class and accuracy unchanged", func(t *testing.T) {
		gm, _, pmcMock := newTestGMClock()
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.ClockClass = fbprotocol.ClockClass6
		gm.announceClockClassIfChanged(
			event.Event{Source: event.GNSS, Data: &event.GNSSData{}},
			SyncState{State: event.PTP_LOCKED, ClockClass: fbprotocol.ClockClass6},
		)
		assert.Empty(t, pmcMock.SnapshotSetCalls())
	})

	t.Run("DPLL holdover computes accuracy from offset", func(t *testing.T) {
		gm, _, pmcMock := newTestGMClock()
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.ClockClass = fbprotocol.ClockClass7
		gm.announceClockClassIfChanged(
			event.Event{Source: event.DPLL, Data: &event.PTPData{Values: map[event.ValueType]interface{}{event.OFFSET: int64(500)}}},
			SyncState{State: event.PTP_HOLDOVER, ClockClass: fbprotocol.ClockClass7},
		)
		setCalls := pmcMock.SnapshotSetCalls()
		require.Len(t, setCalls, 1)
		assert.Equal(t, fbprotocol.ClockClass7, setCalls[0].GMSettings.ClockQuality.ClockClass)
		assert.NotEqual(t, fbprotocol.ClockAccuracyUnknown, setCalls[0].GMSettings.ClockQuality.ClockAccuracy)
	})

	t.Run("updates announcedClockClass on success", func(t *testing.T) {
		gm, _, _ := newTestGMClock()
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.ClockClass = protocol.ClockClassFreerun
		gm.announceClockClassIfChanged(
			event.Event{Source: event.GNSS, Data: &event.GNSSData{}},
			SyncState{State: event.PTP_FREERUN, ClockClass: protocol.ClockClassFreerun},
		)
		assert.Equal(t, protocol.ClockClassFreerun, gm.announcedClockClass)
	})

	t.Run("does not update announcedClockClass on setGMSettings error", func(t *testing.T) {
		gm, _, pmcMock := newTestGMClock()
		pmcMock.SetGMSettingsErr = assert.AnError
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.ClockClass = protocol.ClockClassFreerun
		gm.announceClockClassIfChanged(
			event.Event{Source: event.GNSS, Data: &event.GNSSData{}},
			SyncState{State: event.PTP_FREERUN, ClockClass: protocol.ClockClassFreerun},
		)
		assert.Equal(t, fbprotocol.ClockClass6, gm.announcedClockClass,
			"announcedClockClass must not change when PMC write fails")
	})

	t.Run("updates announcedClockClass to announced value on success", func(t *testing.T) {
		gm, _, _ := newTestGMClock()
		gm.announcedClockClass = fbprotocol.ClockClass6
		gm.announcedClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
		gm.syncState.ClockClass = protocol.ClockClassFreerun
		gm.announceClockClassIfChanged(
			event.Event{Source: event.GNSS, Data: &event.GNSSData{}},
			SyncState{State: event.PTP_FREERUN, ClockClass: protocol.ClockClassFreerun},
		)
		assert.Equal(t, protocol.ClockClassFreerun, gm.announcedClockClass)
	})
}

// --- GM State Machine Tests ---

func TestUpdateGMState(t *testing.T) {
	const cfg = testTS2PHCCfg
	const iface = "ens1f0"

	makeEvent := func(process event.EventSource, state event.PTPState, sourceLost bool) event.Event {
		e := event.Event{
			Source:     process,
			IFace:      iface,
			CfgName:    cfg,
			ClockType:  event.GM,
			Time:       time.Now().UnixMilli(),
			WriteToLog: true,
		}
		if process == event.GNSS {
			var gpsStatus int64
			if state == event.PTP_LOCKED {
				gpsStatus = 3
			}
			e.Data = &event.GNSSData{GPSStatus: gpsStatus, Offset: 0, SourceLost: sourceLost}
		} else {
			e.Data = &event.PTPData{
				State:      state,
				Values:     map[event.ValueType]interface{}{event.OFFSET: int64(0)},
				SourceLost: sourceLost,
			}
		}
		return e
	}

	type step struct {
		events         []event.Event
		wantState      event.PTPState
		wantClockClass fbprotocol.ClockClass
	}

	tests := []struct {
		desc  string
		steps []step
	}{
		{
			desc: "all sources locked",
			steps: []step{
				{
					events: []event.Event{
						makeEvent(event.GNSS, event.PTP_LOCKED, false),
						makeEvent(event.DPLL, event.PTP_LOCKED, false),
						makeEvent(event.TS2PHCProcessName, event.PTP_LOCKED, false),
					},
					wantState:      event.PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
			},
		},
		{
			desc: "DPLL locked, GNSS locked, ts2phc freerun",
			steps: []step{
				{
					events: []event.Event{
						makeEvent(event.GNSS, event.PTP_LOCKED, false),
						makeEvent(event.DPLL, event.PTP_LOCKED, false),
						makeEvent(event.TS2PHCProcessName, event.PTP_FREERUN, false),
					},
					wantState:      event.PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		{
			desc: "DPLL holdover",
			steps: []step{
				{
					events: []event.Event{
						makeEvent(event.GNSS, event.PTP_LOCKED, false),
						makeEvent(event.DPLL, event.PTP_HOLDOVER, false),
						makeEvent(event.TS2PHCProcessName, event.PTP_LOCKED, false),
					},
					wantState:      event.PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			rec := ipcRecorder{}
			gm := &GM{
				cfgName:      cfg,
				sendIPC:      rec.send,
				getUtcOffset: stubUtcOffset,
				pmcClient:    &pmc.MockClient{},
				syncState: SyncState{
					State:         event.PTP_NOTSET,
					ClockClass:    protocol.ClockClassUninitialized,
					ClockAccuracy: fbprotocol.ClockAccuracyUnknown,
				},
				overallSyncState: event.PTP_NOTSET,
			}

			var result SyncState
			for i, s := range tt.steps {
				for _, ev := range s.events {
					result = gm.AddEvent(ev)
				}

				assert.Equal(t, s.wantState, result.State, "step %d: state", i)
				assert.Equal(t, s.wantClockClass, result.ClockClass, "step %d: clockClass", i)
			}
		})
	}
}
