package clock

import (
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testThreshold is the default LOCKED window / holdover timeout used by BC tests:
// LOCKED iff -100 < offset < 100.
var testThreshold = event.PtpClockThreshold{
	MaxOffsetThreshold: 100,
	MinOffsetThreshold: -100,
	HoldOverTimeout:    5,
}

func newTestBCClock() (*BCClock, *ipcRecorder) {
	rio := &ipcRecorder{}
	return &BCClock{
		cfgName:          testPTP4lCfg,
		sendIPC:          rio.send,
		threshold:        testThreshold,
		syncState:        event.PTP_NOTSET,
		overallSyncState: event.PTP_NOTSET,
		osClockState:     event.PTP_NOTSET,
	}, rio
}

// offsetEvent builds a ptp4l offset event carrying only the raw offset fact.
func offsetEvent(iface string, offset int64) event.Event { //nolint:unparam // iface kept for readability at call sites
	return event.Event{
		IFace: iface,
		Data:  &event.PTPData{Values: map[event.ValueType]interface{}{event.OFFSET: offset}},
	}
}

func TestBCClock_AddEvent_StateTransitions(t *testing.T) {
	t.Run("offset in range drives NOTSET to LOCKED and emits ptp_state IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		cs := bc.AddEvent(offsetEvent(testEns7f0, 50))
		assert.Equal(t, event.PTP_LOCKED, cs.State)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypePTPState, rio.messages[0].Type)
		assert.Equal(t, testPTP4lCfg, rio.messages[0].Profile)
		assert.Equal(t, testEns7f0, rio.messages[0].IFace)
		assert.Equal(t, ipc.StateValue{State: ipc.StateLocked}, rio.messages[0].Values)
	})

	t.Run("offset out of range drives LOCKED to FREERUN and emits ptp_state IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_LOCKED
		cs := bc.AddEvent(offsetEvent(testEns7f0, 500))
		assert.Equal(t, event.PTP_FREERUN, cs.State)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.StateFreerun, rio.messages[0].Values.(ipc.StateValue).State)
	})

	t.Run("incoming State field is ignored; offset decides", func(t *testing.T) {
		bc, _ := newTestBCClock()
		// State says LOCKED but the offset is out of range: FREERUN wins.
		cs := bc.AddEvent(event.Event{
			IFace: testEns7f0,
			Data: &event.PTPData{
				State:  event.PTP_LOCKED,
				Values: map[event.ValueType]interface{}{event.OFFSET: int64(9999)},
			},
		})
		assert.Equal(t, event.PTP_FREERUN, cs.State)
	})

	t.Run("duplicate state does not emit ptp_state IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_LOCKED
		cs := bc.AddEvent(offsetEvent(testEns7f0, 10))
		assert.Equal(t, event.PTP_LOCKED, cs.State)
		assert.Empty(t, rio.messages)
	})

	t.Run("nil PTPData returns current state unchanged", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_FREERUN
		cs := bc.AddEvent(event.Event{Data: nil})
		assert.Equal(t, event.PTP_FREERUN, cs.State)
		assert.Empty(t, rio.messages)
	})

	t.Run("PTPData without an offset value is a no-op", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_LOCKED
		cs := bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.PTPData{}})
		assert.Equal(t, event.PTP_LOCKED, cs.State)
		assert.Empty(t, rio.messages)
	})
}

func TestBCClock_Holdover(t *testing.T) {
	t.Run("source lost while LOCKED enters HOLDOVER and arms timer", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.iface = testEns7f0
		bc.syncState = event.PTP_LOCKED
		cs := bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.PTPData{SourceLost: true}})
		assert.Equal(t, event.PTP_HOLDOVER, cs.State)
		assert.NotNil(t, bc.holdoverCancel, "timer should be armed")
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.StateHoldover, rio.messages[0].Values.(ipc.StateValue).State)
		bc.cancelHoldoverTimer()
	})

	t.Run("source lost while not LOCKED drops to FREERUN without arming timer", func(t *testing.T) {
		bc, _ := newTestBCClock()
		bc.syncState = event.PTP_FREERUN
		cs := bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.PTPData{SourceLost: true}})
		assert.Equal(t, event.PTP_FREERUN, cs.State)
		assert.Nil(t, bc.holdoverCancel)
	})

	t.Run("in-range offset cancels the holdover timer and re-locks", func(t *testing.T) {
		bc, _ := newTestBCClock()
		bc.iface = testEns7f0
		bc.syncState = event.PTP_LOCKED
		bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.PTPData{SourceLost: true}})
		require.NotNil(t, bc.holdoverCancel)

		cs := bc.AddEvent(offsetEvent(testEns7f0, 10))
		assert.Equal(t, event.PTP_LOCKED, cs.State)
		assert.Nil(t, bc.holdoverCancel, "fresh offset should cancel the timer")
	})

	t.Run("expiry while still in HOLDOVER transitions to FREERUN", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.iface = testEns7f0
		bc.syncState = event.PTP_HOLDOVER
		cs := bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.HoldoverExpired{}})
		assert.Equal(t, event.PTP_FREERUN, cs.State)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.StateFreerun, rio.messages[0].Values.(ipc.StateValue).State)
	})

	t.Run("expiry after re-lock is a no-op", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.iface = testEns7f0
		bc.syncState = event.PTP_LOCKED
		cs := bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.HoldoverExpired{}})
		assert.Equal(t, event.PTP_LOCKED, cs.State)
		assert.Empty(t, rio.messages)
	})

	t.Run("expiry from a superseded timer does not cut a fresh holdover short", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.iface = testEns7f0

		// Enter holdover: arms timer T1 (generation captured as staleGen).
		bc.syncState = event.PTP_LOCKED
		bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.PTPData{SourceLost: true}})
		require.Equal(t, event.PTP_HOLDOVER, bc.syncState)
		staleGen := bc.holdoverGen

		// Re-lock (cancels T1) then lose the source again (arms T2, a new
		// generation) — this is the flap that supersedes T1.
		bc.AddEvent(offsetEvent(testEns7f0, 10))
		require.Equal(t, event.PTP_LOCKED, bc.syncState)
		bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.PTPData{SourceLost: true}})
		require.Equal(t, event.PTP_HOLDOVER, bc.syncState)
		require.NotEqual(t, staleGen, bc.holdoverGen, "re-arm must use a new generation")

		rio.messages = nil
		// T1's delayed expiry (stale generation) must be ignored so the fresh
		// holdover is left intact instead of dropping to FREERUN.
		cs := bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.HoldoverExpired{Generation: staleGen}})
		assert.Equal(t, event.PTP_HOLDOVER, cs.State)
		assert.Empty(t, rio.messages)

		// T2's own expiry (current generation) is still honored.
		cs = bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.HoldoverExpired{Generation: bc.holdoverGen}})
		assert.Equal(t, event.PTP_FREERUN, cs.State)
		bc.cancelHoldoverTimer()
	})

	t.Run("armed timer fires a HoldoverExpired event", func(t *testing.T) {
		bc, _ := newTestBCClock()
		bc.iface = testEns7f0
		bc.threshold.HoldOverTimeout = 0 // fire (almost) immediately
		got := make(chan event.Event, 1)
		bc.sendEvent = func(ev event.Event) { got <- ev }
		bc.syncState = event.PTP_LOCKED
		bc.AddEvent(event.Event{IFace: testEns7f0, Data: &event.PTPData{SourceLost: true}})

		select {
		case ev := <-got:
			_, ok := ev.Data.(*event.HoldoverExpired)
			assert.True(t, ok, "expected a HoldoverExpired event")
			assert.Equal(t, event.PTP4l, ev.Source)
		case <-time.After(time.Second):
			t.Fatal("timer did not fire HoldoverExpired event")
		}
	})
}

func TestBCClock_UpdateOSClockState(t *testing.T) {
	t.Run("worst of LOCKED and FREERUN is FREERUN", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_LOCKED
		bc.overallSyncState = event.PTP_LOCKED
		bc.SystemClockUpdate(event.PTP_FREERUN)
		assert.Equal(t, event.PTP_FREERUN, bc.overallSyncState)
		assert.Equal(t, event.PTP_FREERUN, bc.osClockState)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeSyncState, rio.messages[0].Type)
		assert.Equal(t, ipc.SyncStateValue{State: ipc.StateFreerun}, rio.messages[0].Values)
	})

	t.Run("worst of LOCKED and LOCKED is LOCKED", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_LOCKED
		bc.overallSyncState = event.PTP_LOCKED
		bc.SystemClockUpdate(event.PTP_LOCKED)
		assert.Equal(t, event.PTP_LOCKED, bc.overallSyncState)
		assert.Empty(t, rio.messages)
	})

	t.Run("worst of HOLDOVER and LOCKED is HOLDOVER", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_HOLDOVER
		bc.overallSyncState = event.PTP_NOTSET
		bc.SystemClockUpdate(event.PTP_LOCKED)
		assert.Equal(t, event.PTP_HOLDOVER, bc.overallSyncState)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeSyncState, rio.messages[0].Type)
	})

	t.Run("no change does not emit IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.syncState = event.PTP_FREERUN
		bc.overallSyncState = event.PTP_FREERUN
		bc.SystemClockUpdate(event.PTP_LOCKED)
		assert.Equal(t, event.PTP_FREERUN, bc.overallSyncState)
		assert.Empty(t, rio.messages)
	})
}

func TestBCClock_UpdateClockClass(t *testing.T) {
	t.Run("change emits clock_class IPC with iface", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.AddEvent(offsetEvent(testEns7f0, 50))
		rio.messages = nil // clear ptp_state IPC from addEvent
		bc.updateClockClass(fbprotocol.ClockClass6)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeClockClass, rio.messages[0].Type)
		assert.Equal(t, testPTP4lCfg, rio.messages[0].Profile)
		assert.Equal(t, testEns7f0, rio.messages[0].IFace)
		assert.Equal(t, ipc.ClockClassValue{ClockClass: 6}, rio.messages[0].Values)
	})

	t.Run("same class does not emit IPC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.clockClass = fbprotocol.ClockClass6
		bc.updateClockClass(fbprotocol.ClockClass6)
		assert.Empty(t, rio.messages)
	})

	t.Run("class change updates stored value", func(t *testing.T) {
		bc, _ := newTestBCClock()
		bc.updateClockClass(fbprotocol.ClockClass7)
		assert.Equal(t, fbprotocol.ClockClass7, bc.clockClass)
		bc.updateClockClass(fbprotocol.ClockClass6)
		assert.Equal(t, fbprotocol.ClockClass6, bc.clockClass)
	})
}

func TestBCClock_Interface(t *testing.T) {
	bc, _ := newTestBCClock()
	assert.Equal(t, event.BC, bc.ClockType())
	assert.Equal(t, testPTP4lCfg, bc.ConfigName())
}

func TestBCClock_ClockType(t *testing.T) {
	t.Run("unset defaults to BC", func(t *testing.T) {
		bc, _ := newTestBCClock()
		assert.Equal(t, event.BC, bc.ClockType())
	})

	t.Run("OC role is reported as OC", func(t *testing.T) {
		bc, _ := newTestBCClock()
		bc.clockType = event.OC
		assert.Equal(t, event.OC, bc.ClockType())
	})

	t.Run("OC clock processes events like BC", func(t *testing.T) {
		bc, rio := newTestBCClock()
		bc.clockType = event.OC
		cs := bc.AddEvent(offsetEvent(testEns7f0, 50))
		assert.Equal(t, event.PTP_LOCKED, cs.State)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypePTPState, rio.messages[0].Type)
	})
}

func TestBCClock_ParentDSUpdate(t *testing.T) {
	t.Run("updates clock class and emits", func(t *testing.T) {
		rio := &ipcRecorder{}
		bc := &BCClock{cfgName: testPTP4lCfg, sendIPC: rio.send}

		parentDS := protocol.ParentDataSet{
			GrandmasterClockClass: 6,
		}
		bc.AddEvent(event.Event{Source: event.PMC, Data: &event.ParentDSData{ParentDataSet: parentDS}})

		assert.Equal(t, fbprotocol.ClockClass(6), bc.clockClass)
		require.Len(t, rio.messages, 1)
		assert.Equal(t, ipc.TypeClockClass, rio.messages[0].Type)
		assert.Equal(t, ipc.ClockClassValue{ClockClass: 6}, rio.messages[0].Values)
	})

	t.Run("unchanged class does not send IPC", func(t *testing.T) {
		rio := &ipcRecorder{}
		bc := &BCClock{cfgName: testPTP4lCfg, sendIPC: rio.send, clockClass: fbprotocol.ClockClass(6)}

		parentDS := protocol.ParentDataSet{
			GrandmasterClockClass: 6,
		}
		bc.AddEvent(event.Event{Source: event.PMC, Data: &event.ParentDSData{ParentDataSet: parentDS}})

		assert.Empty(t, rio.messages, "updateClockClass should no-op on unchanged class")
	})

	t.Run("OC reports SlaveOnly, not the grandmaster class", func(t *testing.T) {
		rio := &ipcRecorder{}
		oc, err := NewBC(testPTP4lCfg, true, event.PtpClockThreshold{})
		require.NoError(t, err)
		oc.SetIPC(rio.send)

		// A parent dataset advertising the grandmaster's class (6) must not
		// override the OC's own 255/SlaveOnly class.
		parentDS := protocol.ParentDataSet{GrandmasterClockClass: 6}
		oc.AddEvent(event.Event{Source: event.PMC, Data: &event.ParentDSData{ParentDataSet: parentDS}})

		assert.Equal(t, fbprotocol.ClockClassSlaveOnly, oc.ClockClass())
		require.Len(t, rio.messages, 1, "OC must publish its class on the first update")
		assert.Equal(t, ipc.TypeClockClass, rio.messages[0].Type)
		assert.Equal(t, ipc.ClockClassValue{ClockClass: uint8(fbprotocol.ClockClassSlaveOnly)}, rio.messages[0].Values)

		// A second, unchanged parent-dataset update dedups (no duplicate IPC).
		rio.messages = nil
		oc.AddEvent(event.Event{Source: event.PMC, Data: &event.ParentDSData{ParentDataSet: parentDS}})
		assert.Empty(t, rio.messages, "unchanged OC class should not re-emit")
	})
}
