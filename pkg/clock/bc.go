package clock

import (
	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

// BCClock is a simple Boundary Clock instance (no DPLL/ts2phc). It also backs
// the Ordinary Clock (OC) role, which is a single slave-port time receiver and
// shares the same state machine; the clockType field records which role it serves.
type BCClock struct {
	cfgName          string
	clockType        event.ClockType
	sendIPC          func(ipc.Message)
	iface            string
	data             []*event.Data
	syncState        event.PTPState
	clockClass       fbprotocol.ClockClass
	overallSyncState event.PTPState
	osClockState     event.PTPState
}

// ClockType returns the clock type for this clock (BC or OC).
func (c *BCClock) ClockType() event.ClockType {
	if c.clockType == event.ClockUnset {
		return event.BC
	}
	return c.clockType
}

// ClockClass returns the current clock class.
func (c *BCClock) ClockClass() fbprotocol.ClockClass { return c.clockClass }

// ConfigName returns the configuration name.
func (c *BCClock) ConfigName() string { return c.cfgName }

// GetState returns the current PTP synchronization state.
func (c *BCClock) GetState() event.PTPState { return c.syncState }

func (c *BCClock) getData(processName event.EventSource) *event.Data {
	for _, d := range c.data {
		if d.ProcessName == processName {
			return d
		}
	}
	d := &event.Data{ProcessName: processName, State: event.PTP_UNKNOWN, Window: *utils.NewWindow(event.WindowSize)}
	c.data = append(c.data, d)
	return d
}

// AddEvent processes an event and updates clock state.
func (c *BCClock) AddEvent(ev event.Event) SyncState {
	switch ev.Source {
	case event.PMC:
		if ds, ok := ev.Data.(*event.ParentDSData); ok {
			clockClass := fbprotocol.ClockClass(ds.ParentDataSet.GrandmasterClockClass)
			c.updateClockClass(clockClass)
		}
		return SyncState{State: c.syncState, LeadingIFace: event.LEADING_INTERFACE_UNKNOWN}
	default:
		d := c.getData(ev.Source)
		d.AddEvent(ev)
		d.UpdateState()

		ptp, ok := ev.Data.(*event.PTPData)
		if !ok || ptp == nil {
			return SyncState{State: c.syncState, LeadingIFace: event.LEADING_INTERFACE_UNKNOWN}
		}

		if c.iface == "" && ev.IFace != "" {
			c.iface = ev.IFace
		}

		prev := c.syncState
		c.syncState = d.State

		if c.syncState != prev {
			c.sendIPC(ipc.Message{
				Type:    ipc.TypePTPState,
				Profile: c.cfgName,
				IFace:   ev.IFace,
				Values:  ipc.StateValue{State: event.PtpStateToIPCState(c.syncState)},
			})
		}

		emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState, c.osClockState, c.cfgName)

		return SyncState{State: c.syncState, LeadingIFace: event.LEADING_INTERFACE_UNKNOWN}
	}
}

// SystemClockUpdate updates the OS clock state.
func (c *BCClock) SystemClockUpdate(osClockState event.PTPState) {
	c.osClockState = osClockState
	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState, c.osClockState, c.cfgName)
}

// Reset resets the clock state.
func (c *BCClock) Reset() {
	c.syncState = event.PTP_FREERUN
	c.overallSyncState = event.PTP_FREERUN
	c.clockClass = 0
	c.data = nil
	c.iface = ""
}

func (c *BCClock) updateClockClass(clockClass fbprotocol.ClockClass) {
	if clockClass == c.clockClass {
		return
	}
	c.clockClass = clockClass
	c.sendIPC(ipc.Message{
		Type:    ipc.TypeClockClass,
		Profile: c.cfgName,
		IFace:   c.iface,
		Values:  ipc.ClockClassValue{ClockClass: uint8(clockClass)},
	})
}
