package clock

import (
	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
)

// HAClock is a High Availability boundary clock: a composite of two or more
// member BC instances fronted by a single phc2sys that fails over between them to
// discipline the system clock (CLOCK_REALTIME).
//
// Unlike a plain BC, HA is about redundancy, so its PTP-side state is the
// best-of its members (LOCKED > HOLDOVER > FREERUN): as long as one member can
// serve as a good source, phc2sys fails over to it and the node stays synced. A
// member in FREERUN only degrades the composite when *every* member is degraded.
//
// The overall (published) sync state then combines that best-of-members state
// with the OS clock the usual worst-of way:
//
//	overall = worstOf(bestOf(members...), osClock)
//
// Member events are routed by config name: each member is its own ptp4l instance
// with its own ptp4l.{runID}.config, so ev.CfgName uniquely identifies the
// member an event belongs to.
type HAClock struct {
	BaseClock
	sendEvent func(event.Event)
	// members is a list of the clocks that make up this composite clock
	members map[string]*BCClock
	// memberProfiles maps a member's config name to its PtpConfig profile name.
	memberProfiles map[string]string
	syncState      event.PTPState
	// activeCfg is the config name of the member phc2sys has selected as the
	// system-clock source, learned from SelectedSourceData events. It is "" until
	// the first selection arrives (no member is reported active until then).
	activeCfg string
}

// HAMember pairs an HA member BC clock with its PtpConfig profile name. The clock
// is routed to by its ConfigName(); the profile name labels the HA status metric.
type HAMember struct {
	Profile string
	Clock   *BCClock
}

// NewHA creates a High Availability clock composed of the given member BC clocks.
// Members are routed to by their ConfigName(). cfgName is the HA profile's own
// config name (the phc2sys-only HA profile), used when publishing the composite
// sync state.
func NewHA(cfgName string, members ...HAMember) (*HAClock, error) {
	c := &HAClock{
		BaseClock:      newBaseClock(cfgName, event.PtpClockThreshold{}),
		members:        map[string]*BCClock{},
		memberProfiles: map[string]string{},
		syncState:      event.PTP_NOTSET,
	}
	for _, m := range members {
		cfg := m.Clock.ConfigName()
		c.members[cfg] = m.Clock
		c.memberProfiles[cfg] = m.Profile
	}
	return c, nil
}

// SetIPC stores the real IPC sender for the HA clock and hands every member an
// interceptor instead of the raw sender, so the HA clock can filter what members
// publish. Members run synchronously inside the HA clock's own AddEvent /
// SystemClockUpdate, so each member message passes through the interceptor inline
// before the HA clock emits its own composite message — there is no ordering or
// cross-thread concern.
func (c *HAClock) SetIPC(f func(message ipc.Message)) {
	c.BaseClock.SetIPC(f)
	for _, m := range c.members {
		m.SetIPC(c.interceptMemberIPC)
	}
}

// interceptMemberIPC filters IPC messages a member BC clock tries to publish.
//
// A member computes its own overall sync state (worstOf member + OS clock) and
// would publish it under the member's config name. Under HA that node-level
// composite belongs to the HA clock alone — if every member also emitted one we
// would publish conflicting sync_state messages for the same node. So member
// sync_state is dropped here; the HA clock emits the single composite sync_state
// (worstOf(bestOf(members), osClock)) under the HA profile.
//
// Per-port state (ptp_state) and clock_class are per-member facts and pass
// through unchanged.
func (c *HAClock) interceptMemberIPC(msg ipc.Message) {
	if msg.Type == ipc.TypeSyncState {
		return
	}
	c.sendIPC(msg)
}

// SetOsClock sets the shared OS clock on the HA clock and all members. Members
// share the HA clock's OS clock because a single phc2sys disciplines CLOCK_REALTIME.
func (c *HAClock) SetOsClock(osClock *OsClock) {
	c.BaseClock.SetOsClock(osClock)
	for _, m := range c.members {
		m.SetOsClock(osClock)
	}
}

// SetEventLoopbackFunc provides the loopback used by members to post their own
// events (e.g. holdover expiry) back onto the event pipeline.
func (c *HAClock) SetEventLoopbackFunc(f func(event.Event)) {
	c.sendEvent = f
	for _, m := range c.members {
		m.SetEventLoopbackFunc(f)
	}
}

// ClockType returns HA.
func (c *HAClock) ClockType() event.ClockType { return event.HA }

// ConfigNames returns the HA clock's own config (which carries source-selection
// events) plus every member's config (member ptp4l events), so the clock manager
// routes them all to this composite.
func (c *HAClock) ConfigNames() []string {
	names := c.BaseClock.ConfigNames()
	for cfg := range c.members {
		names = append(names, cfg)
	}
	return names
}

// ClockClass returns the clock class of the currently active (best) member, or
// the uninitialized class when there are no members.
func (c *HAClock) ClockClass() fbprotocol.ClockClass {
	if m := c.activeMember(); m != nil {
		return m.ClockClass()
	}
	return 0
}

// GetState returns the composite PTP state: the best-of the member states.
func (c *HAClock) GetState() event.PTPState { return c.syncState }

// AddEvent routes an event to the member identified by its config name, then
// recomputes the composite (best-of-members) state and reconciles the overall
// (with-OS-clock) sync state.
func (c *HAClock) AddEvent(ev event.Event) SyncState {
	// A phc2sys source selection tells us which member is actively disciplining the
	// system clock. It carries an interface, not a member config, so it is routed
	// here by the HA phc2sys config name rather than a member config name. It only
	// updates the active member (ha_profile_status); it is not a clock_state sample,
	// so it reports no leading interface and clockmgr emits no clock_state for it.
	if sel, ok := ev.Data.(*event.SelectedSourceData); ok {
		c.setActiveByIface(sel.IFace)
		return SyncState{State: c.syncState, HAProfileStatus: c.haProfileStatus()}
	}

	// Forward the event to the member the event is for
	m, ok := c.members[ev.CfgName]
	if !ok {
		glog.Warningf("HAClock[%s]: no member clock for config %s, dropping event", c.cfgName, ev.CfgName)
		return SyncState{State: c.syncState, HAProfileStatus: c.haProfileStatus()}
	}
	ms := m.AddEvent(ev)

	prev := c.syncState
	c.syncState = c.bestMemberState()
	if c.syncState != prev {
		glog.Infof("HAClock[%s]: composite state %s -> %s", c.cfgName, prev, c.syncState)
	}
	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState, c.osClock.State, c.cfgName)

	ms.HAProfileStatus = c.haProfileStatus()
	return ms
}

// SystemClockUpdate reconciles the overall sync state after an OS clock change
// and forwards the notification to members (which share the OS clock).
func (c *HAClock) SystemClockUpdate() {
	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState, c.osClock.State, c.cfgName)
	for _, m := range c.members {
		m.SystemClockUpdate()
	}
}

// Reset resets the HA clock and all its members.
func (c *HAClock) Reset() {
	for _, m := range c.members {
		m.Reset()
	}
	c.BaseClock.Reset()
	c.syncState = event.PTP_FREERUN
	c.overallSyncState = event.PTP_FREERUN
	c.activeCfg = ""
}

// bestMemberState folds bestOfState across all members. With no members it
// returns PTP_NOTSET.
func (c *HAClock) bestMemberState() event.PTPState {
	state := event.PTP_NOTSET
	for _, m := range c.members {
		state = bestOfState(state, m.GetState())
	}
	return state
}

// setActiveByIface records the member that owns the given interface as the active
// (phc2sys-selected) source. An interface that matches no member is logged and
// ignored, leaving the previous selection in place.
func (c *HAClock) setActiveByIface(iface string) {
	for cfg, m := range c.members {
		if m.iface == iface {
			if c.activeCfg != cfg {
				glog.Infof("HAClock[%s]: active source %s -> %s (%s)", c.cfgName, c.activeCfg, cfg, iface)
				c.activeCfg = cfg
			}
			return
		}
	}
	glog.Warningf("HAClock[%s]: selected source %q matches no member interface", c.cfgName, iface)
}

// activeMember returns the member phc2sys has selected as the system-clock source,
// or nil until the first selection arrives (or if the selected config is unknown).
func (c *HAClock) activeMember() *BCClock {
	if c.activeCfg == "" {
		return nil
	}
	return c.members[c.activeCfg]
}

// currentSyncState reports the composite state, the active member's leading
// interface, and the per-member active/inactive status used for the HA metric.
func (c *HAClock) currentSyncState() SyncState {
	leading := event.LEADING_INTERFACE_UNKNOWN
	if m := c.activeMember(); m != nil {
		leading = m.currentSyncState().LeadingIFace
	}
	return SyncState{
		State:           c.syncState,
		ClockClass:      c.ClockClass(),
		LeadingIFace:    leading,
		HAProfileStatus: c.haProfileStatus(),
	}
}

// haProfileStatus returns each member profile's active/inactive status. The active
// member is the one phc2sys selected as the system-clock source; all others are
// false so their gauge values are cleared. Before the first selection no member is
// active (all false). Returns nil when there are no members.
func (c *HAClock) haProfileStatus() map[string]bool {
	if len(c.members) == 0 {
		return nil
	}
	active := c.activeMember()
	status := make(map[string]bool, len(c.members))
	for cfg, m := range c.members {
		status[c.memberProfiles[cfg]] = m == active
	}
	return status
}

// bestOfState returns the healthier of two states (LOCKED > HOLDOVER > FREERUN).
func bestOfState(a, b event.PTPState) event.PTPState {
	if a == event.PTP_LOCKED || b == event.PTP_LOCKED {
		return event.PTP_LOCKED
	}
	if a == event.PTP_HOLDOVER || b == event.PTP_HOLDOVER {
		return event.PTP_HOLDOVER
	}
	if a == event.PTP_FREERUN || b == event.PTP_FREERUN {
		return event.PTP_FREERUN
	}
	return a // both PTP_NOTSET / PTP_UNKNOWN
}
