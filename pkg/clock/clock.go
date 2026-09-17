package clock

import (
	"fmt"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
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

// NewClock creates the appropriate Clock implementation for the given clock type.
func NewClock(cfgName string, clockType event.ClockType, sendIPC func(ipc.Message), sendEvent func(event.Event), getUtcOffset func() int, pmcClient pmc.Client) (Clock, error) {
	switch clockType {
	case event.GM:
		if pmcClient == nil {
			return nil, fmt.Errorf("pmc.Client is required for clock type %s (config %s)", clockType, cfgName)
		}
		return &GM{
			cfgName:      cfgName,
			sendIPC:      sendIPC,
			getUtcOffset: getUtcOffset,
			pmcClient:    pmcClient,
			syncState: SyncState{
				State:         event.PTP_NOTSET,
				ClockClass:    protocol.ClockClassUninitialized,
				ClockAccuracy: fbprotocol.ClockAccuracyUnknown,
			},
			overallSyncState:       event.PTP_NOTSET,
			osClockState:           event.PTP_NOTSET,
			gnssState:              event.PTP_NOTSET,
			announcedClockClass:    protocol.ClockClassUninitialized,
			announcedClockAccuracy: fbprotocol.ClockAccuracyUnknown,
		}, nil
	case event.TBC:
		if pmcClient == nil {
			return nil, fmt.Errorf("pmc.Client is required for clock type %s (config %s)", clockType, cfgName)
		}
		return &TBC{
			cfgName:      cfgName,
			sendIPC:      sendIPC,
			sendEvent:    sendEvent,
			getUtcOffset: getUtcOffset,
			pmcClient:    pmcClient,
			syncState: SyncState{
				State:         event.PTP_NOTSET,
				ClockClass:    protocol.ClockClassUninitialized,
				ClockAccuracy: fbprotocol.ClockAccuracyUnknown,
			},
			overallSyncState: event.PTP_NOTSET,
			osClockState:     event.PTP_NOTSET,
			leadingClockData: newLeadingClockParams(),
			announceToken:    nextAnnounceToken(),
		}, nil
	case event.BC, event.OC:
		// OC (single slave port) and BC share the same state machine; the
		// clockType is preserved so metrics and events report the correct role.
		return &BCClock{
			cfgName:          cfgName,
			clockType:        clockType,
			sendIPC:          sendIPC,
			syncState:        event.PTP_NOTSET,
			overallSyncState: event.PTP_NOTSET,
			osClockState:     event.PTP_NOTSET,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported clock type %q for config %s", clockType, cfgName)
	}
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
