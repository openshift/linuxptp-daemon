package clock

import (
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
)

// TODO: BCClock is a mash of OC and BC. Should figure out if we want to split them into separate clocks, or rename
//       BCClock to be more generic

// BCClock is a simple Boundary Clock instance (no DPLL/ts2phc). It also backs
// the Ordinary Clock (OC) role, which is a single slave-port time receiver and
// shares the same state machine; the clockType field records which role it serves.
//
// Unlike the T-BC, a BC/OC has a single input: the ptp4l servo offset on its
// slave port. Its state machine is therefore:
//   - offset within [Min, Max]                 -> LOCKED
//   - offset outside [Min, Max]                -> FREERUN
//   - slave port faulty (source lost) while LOCKED -> HOLDOVER (arm timer)
//   - holdover timer expires while HOLDOVER    -> FREERUN
//
// The offset gate and the holdover timer both live here so cancellation stays
// coherent: the same code that decides LOCKED is the code that cancels the timer.
type BCClock struct {
	cfgName          string
	clockType        event.ClockType
	sendIPC          func(ipc.Message)
	sendEvent        func(event.Event)
	threshold        event.PtpClockThreshold
	iface            string
	syncState        event.PTPState
	clockClass       fbprotocol.ClockClass
	overallSyncState event.PTPState
	osClockState     event.PTPState
	// holdoverCancel stops the pending holdover timer goroutine when the clock
	// leaves HOLDOVER (offset recovered, or Reset). nil when no timer is armed.
	holdoverCancel chan struct{}
	// holdoverGen is incremented every time a holdover timer is armed or
	// canceled. The armed timer stamps its generation onto the HoldoverExpired
	// event it posts; a stale event whose generation no longer matches is
	// ignored, so a superseded or canceled timer cannot cut a fresh holdover
	// short.
	holdoverGen uint64
}

// NewBC creates a new Boundary clock. If isOC is true, an Ordinary clock will be created instead
func NewBC(cfgName string, isOC bool, threshold event.PtpClockThreshold) (*BCClock, error) {
	clockType := event.BC
	if isOC {
		clockType = event.OC
	}
	c := &BCClock{
		cfgName:          cfgName,
		clockType:        clockType,
		threshold:        threshold,
		syncState:        event.PTP_NOTSET,
		overallSyncState: event.PTP_NOTSET,
		osClockState:     event.PTP_NOTSET,
	}
	return c, nil
}

// SetIPC sets the IPC sender function
func (c *BCClock) SetIPC(f func(message ipc.Message)) {
	c.sendIPC = f
}

// SetEventLoopbackFunc provides a function for the clock to send its own events into the event pipeline
func (c *BCClock) SetEventLoopbackFunc(f func(event.Event)) {
	c.sendEvent = f
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

// AddEvent processes an event and updates clock state.
func (c *BCClock) AddEvent(ev event.Event) SyncState {
	switch ev.Source {
	case event.PMC:
		if ds, ok := ev.Data.(*event.ParentDSData); ok {
			// A boundary clock reports the upstream grandmaster's clock class. An
			// ordinary (slave-only) clock reports its own class (255/SlaveOnly)
			// and ignores the grandmaster class carried in the parent dataset.
			if c.clockType == event.OC {
				c.updateClockClass(fbprotocol.ClockClassSlaveOnly)
			} else {
				c.updateClockClass(fbprotocol.ClockClass(ds.ParentDataSet.GrandmasterClockClass))
			}
		}
		return c.currentSyncState()
	default:
		// A holdover timer that has fired posts this back onto the loop.
		if he, expired := ev.Data.(*event.HoldoverExpired); expired {
			c.handleHoldoverExpired(he.Generation)
			return c.currentSyncState()
		}

		ptp, ok := ev.Data.(*event.PTPData)
		if !ok || ptp == nil {
			return c.currentSyncState()
		}
		// Record the follower interface once known
		if c.iface == "" && ev.IFace != "" {
			c.iface = ev.IFace
		}

		prev := c.syncState
		switch {
		case ptp.SourceLost:
			// ptp4l lost contact with its master (slave port faulty), so there
			// is no offset to range-check. Coast in HOLDOVER until the timer
			// expires or the source returns; if we were not LOCKED there is
			// nothing to hold, so drop straight to FREERUN.
			if c.syncState == event.PTP_LOCKED {
				c.syncState = event.PTP_HOLDOVER
				c.armHoldoverTimer()
			}
		default:
			offset, hasOffset := ptpOffset(ptp)
			if !hasOffset {
				return c.currentSyncState()
			}
			// We have offset data again, so cancel the timer to enter freerun
			c.cancelHoldoverTimer()
			if isOffsetInRange(offset, c.threshold.MaxOffsetThreshold, c.threshold.MinOffsetThreshold) {
				c.syncState = event.PTP_LOCKED
			} else {
				c.syncState = event.PTP_FREERUN
			}
		}

		c.onStateChanged(prev, ev.IFace)
		return c.currentSyncState()
	}
}

// handleHoldoverExpired runs on the event loop when the holdover timer fires.
// It only drops to FREERUN if we are still in HOLDOVER and the expiry came from
// the currently armed timer generation; if an in-range offset re-locked us (or a
// re-lock then a fresh holdover superseded the timer) while the expiry event was
// in flight, it is a no-op.
func (c *BCClock) handleHoldoverExpired(gen uint64) {
	if c.syncState != event.PTP_HOLDOVER || gen != c.holdoverGen {
		return
	}
	prev := c.syncState
	c.syncState = event.PTP_FREERUN
	c.holdoverCancel = nil
	glog.Infof("BCClock[%s]: holdover expired, transitioning to FREERUN", c.cfgName)
	c.onStateChanged(prev, c.iface)
}

// armHoldoverTimer (re)starts the holdover timer. On expiry it posts a
// HoldoverExpired event back onto the loop rather than mutating state directly.
func (c *BCClock) armHoldoverTimer() {
	c.cancelHoldoverTimer()
	c.holdoverGen++
	gen := c.holdoverGen
	cancel := make(chan struct{})
	c.holdoverCancel = cancel
	timeout := time.Duration(c.threshold.HoldOverTimeout) * time.Second
	iface := c.iface
	glog.Infof("BCClock[%s]: entering HOLDOVER, timer %s", c.cfgName, timeout)
	go func() {
		select {
		case <-cancel:
			return
		case <-time.After(timeout):
			if c.sendEvent == nil {
				return
			}
			c.sendEvent(event.Event{
				Source:    event.PTP4l,
				CfgName:   c.cfgName,
				IFace:     iface,
				ClockType: c.ClockType(),
				Time:      time.Now().UnixMilli(),
				Data:      &event.HoldoverExpired{Generation: gen},
			})
		}
	}()
}

// cancelHoldoverTimer stops a pending holdover timer, if any. Safe to call when
// no timer is armed. Bumping the generation invalidates any expiry event the
// canceled timer may have already queued onto the event loop.
func (c *BCClock) cancelHoldoverTimer() {
	if c.holdoverCancel != nil {
		close(c.holdoverCancel)
		c.holdoverCancel = nil
		c.holdoverGen++
	}
}

// onStateChanged emits the per-port state IPC on a transition and always
// reconciles the overall (with-OS-clock) sync state.
func (c *BCClock) onStateChanged(prev event.PTPState, iface string) {
	if c.syncState != prev {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypePTPState,
			Profile: c.cfgName,
			IFace:   iface,
			Values:  ipc.StateValue{State: event.PtpStateToIPCState(c.syncState)},
		})
	}
	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState, c.osClockState, c.cfgName)
}

// currentSyncState returns the clock's state. LeadingIFace is only reported once
// the follower interface is known, so clockmgr does not emit clock_state before
// then.
func (c *BCClock) currentSyncState() SyncState {
	leading := event.LEADING_INTERFACE_UNKNOWN
	if c.iface != "" {
		leading = c.iface
	}
	return SyncState{State: c.syncState, ClockClass: c.clockClass, LeadingIFace: leading}
}

// SystemClockUpdate updates the OS clock state.
func (c *BCClock) SystemClockUpdate(osClockState event.PTPState) {
	c.osClockState = osClockState
	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState, c.osClockState, c.cfgName)
}

// Reset resets the clock state.
func (c *BCClock) Reset() {
	c.cancelHoldoverTimer()
	c.syncState = event.PTP_FREERUN
	c.overallSyncState = event.PTP_FREERUN
	// Clear the last-published class so the next PMC update re-emits it and
	// repopulates the CEP cache.
	c.clockClass = 0
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

// isOffsetInRange reports whether the ptp4l servo offset is within the
// configured LOCKED window: Min < offset < Max.
func isOffsetInRange(offset, maxOffset, minOffset int64) bool {
	return offset < maxOffset && offset > minOffset
}

// ptpOffset extracts the OFFSET value from PTP data, if present.
func ptpOffset(ptp *event.PTPData) (int64, bool) {
	if v, ok := ptp.Values[event.OFFSET]; ok {
		if i, isInt := v.(int64); isInt {
			return i, true
		}
	}
	return 0, false
}
