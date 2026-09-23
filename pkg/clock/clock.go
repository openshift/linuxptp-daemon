package clock

import (
	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
)

// Clock represents a PTP clock instance tied to a specific config profile.
type Clock interface {
	AddEvent(ev event.Event) SyncState
	GetState() event.PTPState
	SystemClockUpdate(state event.PTPState)
	Reset()
	ConfigName() string
	ClockType() event.ClockType
	ClockClass() fbprotocol.ClockClass
	SetIPC(func(message ipc.Message))
	SetEventLoopbackFunc(f func(event.Event))
}

// SyncState holds the composite synchronization state of a clock.
type SyncState struct {
	State          event.PTPState
	ClockClass     fbprotocol.ClockClass
	SourceLost     bool
	ClkLog         string
	LastLoggedTime int64
	LeadingIFace   string
	ClockAccuracy  fbprotocol.ClockAccuracy
	ClockOffset    int64
}

func emitOverallSyncStateIfChanged(sendIPC func(ipc.Message), overallSyncState *event.PTPState, clockState, osClockState event.PTPState, profile string) {
	next := worstOfState(clockState, osClockState)
	if next != *overallSyncState {
		*overallSyncState = next
		sendIPC(ipc.Message{
			Type:    ipc.TypeSyncState,
			Profile: profile,
			Values:  ipc.SyncStateValue{State: event.PtpStateToIPCState(next)},
		})
	}
}

func worstOfState(a, b event.PTPState) event.PTPState {
	if a == event.PTP_NOTSET || b == event.PTP_NOTSET {
		return event.PTP_NOTSET
	}
	if a == event.PTP_FREERUN || b == event.PTP_FREERUN {
		return event.PTP_FREERUN
	}
	if a == event.PTP_HOLDOVER || b == event.PTP_HOLDOVER {
		return event.PTP_HOLDOVER
	}
	return event.PTP_LOCKED
}
