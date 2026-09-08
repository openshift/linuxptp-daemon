package dpll

import (
	"context"
	"fmt"
	"math"

	"strconv"
	"sync"
	"time"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	nl "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/mdlayher/genetlink"
	"golang.org/x/sync/semaphore"
)

const (
	DPLL_UNKNOWN       = -1
	DPLL_INVALID       = 0
	DPLL_FREERUN       = 1
	DPLL_LOCKED        = 2
	DPLL_LOCKED_HO_ACQ = 3
	DPLL_HOLDOVER      = 4

	LocalMaxHoldoverOffSet = 1500  // ns
	LocalHoldoverTimeout   = 14400 // secs
	MaxInSpecOffset        = 1500  // ns
	monitoringInterval     = 1 * time.Second

	LocalMaxHoldoverOffSetStr = "LocalMaxHoldoverOffSet"
	LocalHoldoverTimeoutStr   = "LocalHoldoverTimeout"
	MaxInSpecOffsetStr        = "MaxInSpecOffset"
	ClockIdStr                = "clockId"
	FaultyPhaseOffset         = 99999999999

	// The PPS pin index in the pin-parent-device structure
	PPS_PIN_INDEX = 1
)

// Flag is a bitmask which changes the default DPLL monitoeing  behavior
type Flag uint64

const (
	// FlagNoPhaseOffset allows skipping phase offset monitoring
	FlagNoPhaseOffset Flag = (1 << 0)
	// FlagNoPhaseStatus allows skipping phase status (pps lock/unlock) monitoring
	FlagNoPhaseStatus Flag = (1 << 1)
	// FlagNoFreqencyStatus allows skipping frequency status (eec lock/unlock) monitoring
	FlagNoFreqencyStatus Flag = (1 << 2)

	// FlagOnlyPhaseStatus represents a DPll with only phase status (pps lock/unlock)
	FlagOnlyPhaseStatus Flag = FlagNoFreqencyStatus | FlagNoPhaseOffset
)

func stateName(state int64) string {
	switch state {
	case DPLL_INVALID:
		return "INVALID"
	case DPLL_FREERUN:
		return "FREERUN"
	case DPLL_LOCKED:
		return "LOCKED"
	case DPLL_LOCKED_HO_ACQ:
		return "LOCKED_HO_ACQ"
	case DPLL_HOLDOVER:
		return "HOLDOVER"
	default:
		return "UNKNOWN"
	}
}

type dpllApiType string

var MockDpllReplies chan *nl.DoDeviceGetReply

const (
	NETLINK dpllApiType = "netlink"
	MOCK    dpllApiType = "mock"
	NONE    dpllApiType = "none"
)

// DpllConfig ... DPLL configuration
type DpllConfig struct {
	LocalMaxHoldoverOffSet uint64
	LocalHoldoverTimeout   uint64
	MaxInSpecOffset        uint64
	iface                  string
	name                   string
	slope                  float64
	timer                  int64 // secs
	inSpec                 bool
	frequencyTraceable     bool
	state                  event.PTPState
	processState           process.State
	onHoldover             bool
	closing                bool
	sourceLost             bool
	processConfig          config.ProcessConfig
	profile                *ptpv1.PtpProfile
	dependsOn              []event.EventSource
	stopCh                 chan struct{}
	holdoverCloseCh        chan bool
	ticker                 *time.Ticker
	apiType                dpllApiType
	conn                   *nl.Conn
	// We need to keep latest DPLL status values, since Netlink device
	// change indications don't contain all the status fields, but
	// only the changed one(s)
	phaseStatus     int64
	frequencyStatus int64
	phaseOffset     int64

	// clockId is needed to distinguish between DPLL associated with the particular
	// iface from other DPLL units that might be present on the system. Clock ID implementation
	// is driver-specific and vendor-specific.
	clockId uint64
	sync.RWMutex
	wg                       sync.WaitGroup
	phaseOffsetPinFilter     map[string]map[string]string
	inSyncConditionThreshold uint64
	inSyncConditionTimes     uint64
	// hardwareConfigHandler is called when device notifications are received
	// All logic for processing device notifications is handled by the hardwareconfig layer
	hardwareConfigHandler func(devices []*nl.DoDeviceGetReply) error

	// Some DPLLs (Carter Flats, for one) do not have both pps (phase) and eec (frequency) states.
	flags Flag

	// devices holds the cache of DPLL device replies
	devices []*nl.DoDeviceGetReply
}

func (d *DpllConfig) InSpec() bool {
	return d.inSpec
}

// DependsOn ...  depends on other events
func (d *DpllConfig) DependsOn() []event.EventSource {
	return d.dependsOn
}

// SetDependsOn ... set depends on ..
func (d *DpllConfig) SetDependsOn(dependsOn []event.EventSource) {
	d.dependsOn = dependsOn
}

// PTPState returns the DPLL clock lock state (FREERUN/LOCKED/HOLDOVER).
func (d *DpllConfig) PTPState() event.PTPState {
	return d.state
}

// SetPhaseOffset ...  set phaseOffset
// Measured phase offset values are fractional with 3-digit decimal places and shall be
// divided by DPLL_PIN_PHASE_OFFSET_DIVIDER to get integer part
// The units are picoseconds.
// We further divide it by 1000 to report nanoseconds
func (d *DpllConfig) SetPhaseOffset(phaseOffset int64) {
	d.phaseOffset = int64(math.Round(float64(phaseOffset / nl.DpllPhaseOffsetDivider / 1000)))
}

// SourceLost ... get source status
func (d *DpllConfig) SourceLost() bool {
	return d.sourceLost
}

// SetSourceLost ... set source status
func (d *DpllConfig) SetSourceLost(sourceLost bool) {
	d.sourceLost = sourceLost
}

// SetHardwareConfigHandler sets the callback function to be invoked when device notifications are received.
// The handler receives all device notifications and is responsible for all matching logic.
func (d *DpllConfig) SetHardwareConfigHandler(handler func(devices []*nl.DoDeviceGetReply) error) {
	d.hardwareConfigHandler = handler
}

// PhaseOffset ... get phase offset
func (d *DpllConfig) PhaseOffset() int64 {
	return d.phaseOffset
}

func (d *DpllConfig) hasFlag(flag Flag) bool {
	return (d.flags & flag) == flag
}

func (d *DpllConfig) flagsToStrings() []string {
	result := make([]string, 0)
	if d.hasFlag(FlagNoFreqencyStatus) {
		result = append(result, "NoFrequencyStatus")
	}
	if d.hasFlag(FlagNoPhaseStatus) {
		result = append(result, "NoPhaseStatus")
	}
	if d.hasFlag(FlagNoPhaseOffset) {
		result = append(result, "NoPhaseOffset")
	}
	return result
}

func (d *DpllConfig) phaseOffsetStr() string {
	if d.hasFlag(FlagNoPhaseOffset) {
		return "UNKNOWN"
	}
	return fmt.Sprintf("%d", d.phaseOffset)
}

// FrequencyStatus ... get frequency status
func (d *DpllConfig) FrequencyStatus() int64 {
	return d.frequencyStatus
}

// PhaseStatus get phase status
func (d *DpllConfig) PhaseStatus() int64 {
	return d.phaseStatus
}

// Name ... name of the process
func (d *DpllConfig) Name() string {
	return string(event.DPLL)
}

// ConfigName ...
func (d *DpllConfig) ConfigName() string {
	return d.processConfig.ConfigName
}

// Conditions returns the action conditions for the DpllConfig process.
func (d *DpllConfig) Conditions() map[process.Action]process.Condition {
	return map[process.Action]process.Condition{}
}

// State returns the current process state of DpllConfig.
func (d *DpllConfig) State() process.State {
	d.RLock()
	defer d.RUnlock()
	return d.processState
}

func (d *DpllConfig) setProcessState(s process.State) {
	d.Lock()
	d.processState = s
	d.Unlock()
}

// Profile returns the PTP profile associated with DpllConfig.
func (d *DpllConfig) Profile() *ptpv1.PtpProfile {
	return d.profile
}

// ClockType returns the clock type of DpllConfig.
func (d *DpllConfig) ClockType() event.ClockType {
	return d.processConfig.ClockType
}

// DependentProcesses returns processes that depend on DpllConfig.
func (d *DpllConfig) DependentProcesses() []process.Process {
	return []process.Process{}
}

// Stopped ... stopped
func (d *DpllConfig) Stopped() bool {
	st := d.State()
	return st == process.Stopped || st == process.Dead
}

// ExitCh ... exit channel
func (d *DpllConfig) ExitCh() chan struct{} {
	return d.stopCh
}

// hasGNSSSAsSource returns whether or not DPLL has GNSS as a source
func (d *DpllConfig) hasGNSSAsSource() bool {
	if d.dependsOn[0] == event.GNSS {
		return true
	}
	return false
}

// hasPPSAsSource returns whether or not DPLL has PPS as a source
func (d *DpllConfig) hasPPSAsSource() bool {
	return d.dependsOn[0] == event.PPS
}

// hasPTPAsSource returns whether or not DPLL has PTP as a source
func (d *DpllConfig) hasPTPAsSource() bool {
	return d.dependsOn[0] == event.PTP4l
}

// hasLeadingSource returns whether or not DPLL is a leading source
func (d *DpllConfig) hasLeadingSource() bool {
	return d.hasPTPAsSource() || d.hasGNSSAsSource()
}

// IFace returns the network interface associated with DpllConfig.
func (d *DpllConfig) IFace() string {
	d.RLock()
	defer d.RUnlock()
	return d.iface
}

// Stop stops DPLL monitoring.
func (d *DpllConfig) Stop() error {
	glog.Infof("stopping %s", d.Name())
	d.Lock()
	if d.processState == process.Created || d.processState == process.Stopping || d.processState == process.Stopped || d.processState == process.Dead {
		d.Unlock()
		return nil
	}
	d.processState = process.Stopping
	stopCh := d.stopCh
	d.Unlock()

	if d.ticker != nil {
		d.ticker.Stop()
		glog.Infof("Ticker stopped %s", d.Name())
	}
	if stopCh != nil {
		close(stopCh)
	}
	d.wg.Wait()
	glog.Infof("Process %s terminated", d.Name())
	return nil
}

// SyncInitialState does a one-shot synchronous DPLL device dump and sends an
// initial state event. It must be called after NewDpll has set up the
// EventChannel. This closes the race window between profile application
// (which sends a Reset that wipes all DPLL event data) and the asynchronous
// MonitorDpllNetlink goroutine completing its own initial dump. Without this,
// hardware-slaved DPLLs (e.g. E830 CF cards) may be absent from the event
// data when the first SourceLost event from the leader DPLL arrives.
func (d *DpllConfig) SyncInitialState() {
	if d.apiType != NETLINK {
		return
	}
	if d.processConfig.EventChannel == nil {
		glog.Infof("SyncInitialState: EventChannel not set for %s, skipping", d.iface)
		return
	}
	conn, err := nl.Dial(nil)
	if err != nil {
		glog.Infof("SyncInitialState: failed to dial netlink for %s: %v", d.iface, err)
		return
	}
	defer conn.Close()
	replies, err := conn.DumpDeviceGet()
	if err != nil {
		glog.Infof("SyncInitialState: failed to dump DPLL devices for %s: %v", d.iface, err)
		return
	}
	glog.Infof("SyncInitialState: pre-populating DPLL state for %s (%d devices)", d.iface, len(replies))
	d.applyStateUpdate(replies, nil)
}

// ProcessStatus ... process status
func (d *DpllConfig) ProcessStatus(_ int64) {
}

func (d *DpllConfig) emitProcessStatus(status int64) {
	if d.processConfig.EventChannel == nil {
		return
	}
	d.processConfig.EventChannel <- event.ProcessStatusEvent(event.DPLL, d.processConfig.ConfigName, d.processConfig.ClockType, d.iface, status)
}

// Start begins DPLL monitoring.
func (d *DpllConfig) Start(_ context.Context) error {
	d.Lock()
	st := d.processState
	switch st {
	case process.Starting, process.Running:
		d.Unlock()
		return nil
	case process.Stopping:
		d.Unlock()
		return fmt.Errorf("%s is stopping", d.Name())
	}
	d.stopCh = make(chan struct{})
	d.processState = process.Starting
	d.Unlock()

	d.run()
	return nil
}

func (d *DpllConfig) run() {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() {
			if d.State() == process.Stopping {
				d.setProcessState(process.Stopped)
			} else {
				d.setProcessState(process.Dead)
			}
			d.emitProcessStatus(0)
		}()
		d.setProcessState(process.Running)
		d.emitProcessStatus(1)
		d.MonitorDpll()
	}()
}

// NewDpll ... create new DPLL process
func NewDpll(clockId uint64, localMaxHoldoverOffSet, localHoldoverTimeout, maxInSpecOffset uint64,
	iface string, dependsOn []event.EventSource, apiType dpllApiType, phaseOffsetPinFilter map[string]map[string]string,
	inSyncConditionTh uint64, inSyncConditionTimes uint64, dpllFlags Flag,
	processCfg config.ProcessConfig, nodeProfile *ptpv1.PtpProfile,
) *DpllConfig {
	glog.Infof("Calling NewDpll with clockId %x, localMaxHoldoverOffSet=%d, localHoldoverTimeout=%d, maxInSpecOffset=%d, iface=%s, phase offset pin filter=%v", clockId, localMaxHoldoverOffSet, localHoldoverTimeout, maxInSpecOffset, iface, phaseOffsetPinFilter)
	d := &DpllConfig{
		clockId:                clockId,
		LocalMaxHoldoverOffSet: localMaxHoldoverOffSet,
		LocalHoldoverTimeout:   localHoldoverTimeout,
		MaxInSpecOffset:        maxInSpecOffset,
		slope: func() float64 {
			return float64(localMaxHoldoverOffSet) / float64(localHoldoverTimeout)
		}(),
		timer:                    0,
		state:                    event.PTP_FREERUN,
		iface:                    iface,
		onHoldover:               false,
		closing:                  false,
		sourceLost:               false,
		frequencyTraceable:       false,
		dependsOn:                dependsOn,
		ticker:                   time.NewTicker(monitoringInterval),
		apiType:                  apiType,
		phaseOffsetPinFilter:     phaseOffsetPinFilter,
		phaseOffset:              FaultyPhaseOffset,
		inSyncConditionThreshold: inSyncConditionTh,
		inSyncConditionTimes:     inSyncConditionTimes,
		flags:                    dpllFlags,
		processConfig:            processCfg,
		profile:                  nodeProfile,
	}

	if d.flags != 0 {
		flagStrings := d.flagsToStrings()
		glog.Warningf("Partial monitoring detected for %s clockId %#x: %v", iface, clockId, flagStrings)
	}

	// time to reach maxnInSpecOffset
	d.timer = int64(math.Round(float64(d.MaxInSpecOffset) / d.slope))
	glog.Infof("slope %f ns/s, in spec offset %d ns, in spec timer %d /sec Max timer %d /s",
		d.slope, d.MaxInSpecOffset, d.timer, d.LocalHoldoverTimeout)
	if d.apiType != MOCK { // use mock type unit test DPLL
		d.setAPIType()
	}
	glog.Infof("api type %v", d.apiType)
	return d
}

func (d *DpllConfig) Slope() float64 {
	return d.slope
}

func (d *DpllConfig) Timer() int64 {
	return d.timer
}

// ActivePhaseOffsetPin checks whether the given pin is actively connected
// and feeds the relevant PPS DPLL matched by clock ID
func (d *DpllConfig) ActivePhaseOffsetPin(pin *nl.PinInfo) (int, bool) {
	if pin.ClockID != d.clockId {
		return -1, false
	}
	for i, p := range pin.ParentDevice {
		// Legacy behavior: An input pin was considered "connected" if it acted as a source.
		// New behavior: An input pin is the device synchronization source when its OperState is "active".
		// Note: In this scenario, the administrative state never exceeds "selectable".
		if (p.State != nl.PinStateConnected && p.Operstate != nl.PinOperstateActive) || p.Direction != nl.PinDirectionInput {
			glog.V(14).Infof("pin id %d parentID=%d skipped: direction=%s adminState=%s operstate=%s",
				pin.ID, p.ParentID, nl.GetPinDirection(p.Direction), nl.GetPinState(p.State), nl.GetPinOperstate(p.Operstate))
			continue
		}
		for _, dev := range d.devices {
			if dev.ID == p.ParentID && dev.ClockID == d.clockId && nl.GetDpllType(dev.Type) == "pps" {
				return i, true
			}
		}
	}
	return -1, false
}

// nlUpdateState updates DPLL state in the DpllConfig structure.
func (d *DpllConfig) nlUpdateState(devices []*nl.DoDeviceGetReply, pins []*nl.PinInfo) bool {
	valid := false

	for _, reply := range devices {
		if reply.ClockID == d.clockId {
			replyHr, err := nl.GetDpllStatusHR(reply, time.Now())
			if err != nil || reply.LockStatus == DPLL_INVALID {
				glog.Info("discarding on invalid lock status: ", replyHr)
				continue
			}
			glog.Info(string(replyHr), " ", d.iface)
			dpllType := nl.GetDpllType(reply.Type)
			lockChanged := false
			switch dpllType {
			case "eec":
				lockChanged = d.frequencyStatus != int64(reply.LockStatus)
				d.frequencyStatus = int64(reply.LockStatus)
				glog.Infof("%s (%#x) updating eec to %s (%d)", d.iface, d.clockId, stateName(d.frequencyStatus), d.frequencyStatus)
				valid = true
			case "pps":
				lockChanged = d.phaseStatus != int64(reply.LockStatus)
				d.phaseStatus = int64(reply.LockStatus)
				glog.Infof("%s (%#x) updating pps to %s (%d)", d.iface, d.clockId, stateName(d.phaseStatus), d.phaseStatus)
				valid = true
			default:
				glog.Infof("discarding irrelevant dpll type: %s, id %#x", dpllType, d.clockId)
			}
			// Log only the parents of the DPLL that changed (reply.ID), and only
			// when lock status actually changes. Otherwise SyncInitialState and
			// MonitorDpllNetlink's initial dump reprint the same EEC+PPS tables.
			if lockChanged {
				nl.LogPinTable(fmt.Sprintf("%s %s->%s", d.iface, dpllType, stateName(int64(reply.LockStatus))),
					reply.ClockID, reply.ID, reply.LockStatus)
			}
		}
	}
	for _, pin := range pins {
		if index, ok := d.ActivePhaseOffsetPin(pin); ok {
			d.SetPhaseOffset(pin.ParentDevice[index].PhaseOffset)
			glog.Info("setting phase offset to ", d.phaseOffset, " ns for clock id ", d.clockId, " iface ", d.iface)
			valid = true
		}
	}
	return valid
}

// applyStateUpdate atomically updates DPLL state from device/pin data and
// re-evaluates the state machine. The lock ensures no concurrent goroutine
// can observe or mutate a partially-updated DpllConfig.
func (d *DpllConfig) applyStateUpdate(devices []*nl.DoDeviceGetReply, pins []*nl.PinInfo) {
	d.Lock()
	defer d.Unlock()
	if d.nlUpdateState(devices, pins) {
		d.stateDecision()
	}
}

// monitorNtf receives a multicast unsolicited notification and
// calls dpll state updating function.
func (d *DpllConfig) monitorNtf(c *genetlink.Conn) {
	for {
		msgs, _, err := c.Receive()
		if err != nil {
			if err.Error() == "netlink receive: use of closed file" {
				glog.Infof("netlink connection has been closed - stop monitoring for %s", d.iface)
			} else {
				glog.Error(err)
			}
			return
		}
		devices, pins := []*nl.DoDeviceGetReply{}, []*nl.PinInfo{}
		for _, msg := range msgs {
			devices, pins = []*nl.DoDeviceGetReply{}, []*nl.PinInfo{}
			switch msg.Header.Command {
			case nl.DpllCmdDeviceChangeNtf:
				devices, err = nl.ParseDeviceReplies([]genetlink.Message{msg})
				if err != nil {
					glog.Error(err)
					return
				}
			case nl.DpllCmdPinChangeNtf:
				pins, err = nl.ParsePinReplies([]genetlink.Message{msg})
				if err != nil {
					glog.Error(err)
					return
				}
			default:
				glog.Info("unhandled dpll message", msg.Header.Command, msg.Data)

			}
		}
		// Pass device notifications to hardwareconfig handler if present
		// All logic (clock ID matching, lock status checking) happens in hardwareconfig layer
		if len(devices) > 0 && d.hardwareConfigHandler != nil {
			if err = d.hardwareConfigHandler(devices); err != nil {
				glog.Errorf("hardwareconfig handler error: %v", err)
			}
		}
		d.applyStateUpdate(devices, pins)
	}
}

func (d *DpllConfig) isNetLinkPresent() bool {
	conn, err := nl.Dial(nil)
	if err != nil {
		glog.Infof("failed to establish dpll netlink connection (%s): %s", d.iface, err)
		return false
	}
	conn.Close()
	return true
}

func (d *DpllConfig) setAPIType() {
	if d.isNetLinkPresent() {
		d.apiType = NETLINK
	} else {
		d.apiType = NONE
	}
}

func (d *DpllConfig) MonitorDpllMock() {
	glog.Info("starting dpll mock monitoring")

	d.applyStateUpdate([]*nl.DoDeviceGetReply{<-MockDpllReplies}, []*nl.PinInfo{})

	glog.Infof("closing dpll mock ")
}

// MonitorDpllNetlink monitors DPLL through netlink
func (d *DpllConfig) MonitorDpllNetlink() {
	redial := true
	var replies []*nl.DoDeviceGetReply
	var err error
	var sem *semaphore.Weighted
	for {
		if redial {
			if d.conn == nil {
				if conn, err2 := nl.Dial(nil); err2 != nil {
					d.conn = nil
					glog.Infof("failed to establish dpll netlink connection (%s): %s", d.iface, err2)
					goto checkExit
				} else {
					d.conn = conn
				}
			}

			c := d.conn.GetGenetlinkConn()
			mcastID, found := d.conn.GetMcastGroupID(nl.DpllMCGRPMonitor)
			if !found {
				glog.Warning("multicast ID ", nl.DpllMCGRPMonitor, " not found")
				goto abort
			}

			replies, err = d.conn.DumpDeviceGet()
			if err != nil {
				goto abort
			}

			d.devices = replies

			d.applyStateUpdate(replies, []*nl.PinInfo{})

			err = c.JoinGroup(mcastID)
			if err != nil {
				goto abort
			}

			sem = semaphore.NewWeighted(1)
			err = sem.Acquire(context.Background(), 1)
			if err != nil {
				goto abort
			}

			go func() {
				defer sem.Release(1)
				d.monitorNtf(c)
			}()

			goto checkExit

		abort:
			d.stopDpll()
		}

	checkExit:
		select {
		case <-d.stopCh:
			glog.Infof("terminating netlink dpll monitoring")
			select {
			case d.processConfig.EventChannel <- event.Event{
				Source:    event.DPLL,
				IFace:     d.iface,
				CfgName:   d.processConfig.ConfigName,
				ClockType: d.processConfig.ClockType,
				Time:      time.Now().UnixMilli(),
				Reset:     true,
			}:
			default:
				glog.Error("failed to send dpll event terminated event")
			}
			d.stopDpll()
			// Allow generated events some time to get processed
			time.Sleep(time.Second)
			if d.onHoldover {
				close(d.holdoverCloseCh)
				glog.Infof("closing holdover for %s", d.iface)
				d.onHoldover = false
				d.closing = true
			}

			return

		default:
			redial = func() bool {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
				defer cancel()

				if sem == nil {
					return false
				}

				if err = sem.Acquire(ctx, 1); err != nil {
					return false
				}

				glog.Infof("dpll monitoring exited, initiating redial (%s)", d.iface)
				d.stopDpll()
				return true
			}()
			time.Sleep(time.Millisecond * 250) // cpu saver
		}
	}
}

// stopDpll stops DPLL monitoring
func (d *DpllConfig) stopDpll() {
	if d.conn != nil {
		if err := d.conn.Close(); err != nil {
			glog.Errorf("error closing DPLL netlink connection: (%s) %s", d.iface, err)
		}
		d.conn = nil
	}
}

// MonitorDpll monitors DPLL on the discovered API, if any
func (d *DpllConfig) MonitorDpll() {
	fmt.Println(d.apiType)
	if d.apiType == MOCK {
		d.RLock()
		stopCh := d.stopCh
		d.RUnlock()
		if stopCh != nil {
			<-stopCh
		}
		return
	} else if d.apiType == NETLINK {
		d.MonitorDpllNetlink()
	} else {
		glog.Errorf("dpll monitoring is not possible, netlink implementation is not present")
		return
	}
}

// stateDecision
func (d *DpllConfig) stateDecision() {
	dpllStatus := d.getDpllState()

	switch dpllStatus {
	case DPLL_FREERUN, DPLL_INVALID, DPLL_UNKNOWN:
		d.inSpec = false
		d.sourceLost = true
		glog.Infof("%s dpll with %s source is in FREERUN", d.iface, d.dependsOn[0])
		if d.hasLeadingSource() && d.onHoldover {
			glog.Infof("trying to close holdover (%s)", d.iface)
			select {
			case d.holdoverCloseCh <- true:
				glog.Infof("closing holdover for %s since DPLL flipped to Unlocked", d.iface)
			default:
			}
		}
		d.state = event.PTP_FREERUN
		d.phaseOffset = FaultyPhaseOffset

	case DPLL_HOLDOVER:
		switch {
		case !d.hasLeadingSource():
			d.state = event.PTP_FREERUN
			d.phaseOffset = FaultyPhaseOffset
			glog.Infof("non-leading DPLL %s is in holdover, reporting FREERUN", d.iface)
		// TODO: GNSS holdover currently doesn't offer holdover out of spec. Tech Debt: should work the same as T-BC
		// making transitions programmable by users
		case !d.inSpec || (d.hasPTPAsSource() && math.Abs(float64(d.PhaseOffset())) > float64(LocalMaxHoldoverOffSet)):
			glog.Infof("leading DPLL %s holdover out of spec (inSpec=%v, offset=%d, max=%d), state is FREERUN",
				d.iface, d.inSpec, d.PhaseOffset(), LocalMaxHoldoverOffSet)
			d.state = event.PTP_FREERUN
			d.phaseOffset = FaultyPhaseOffset
			d.sourceLost = true
			select {
			case d.holdoverCloseCh <- true:
				glog.Infof("closing holdover for %s since holdover is out of spec", d.iface)
			default:
			}
		case !d.onHoldover && !d.closing:
			d.holdoverCloseCh = make(chan bool)
			d.onHoldover = true
			d.state = event.PTP_HOLDOVER
			glog.Infof("starting holdover (%s)", d.iface)
			go d.holdover()
		}

	case DPLL_LOCKED_HO_ACQ, DPLL_LOCKED:
		if d.isOffsetInRange() {
			glog.Infof("%s dpll is locked, source is not lost, offset is in range, state is DPLL_%s", d.iface, stateName(dpllStatus))
			if d.hasLeadingSource() && d.onHoldover {
				select {
				case d.holdoverCloseCh <- true:
					glog.Infof("closing holdover for %s since source is restored and locked ", d.iface)
				default:
				}
			}
			d.inSpec = true
			d.sourceLost = false
			d.state = event.PTP_LOCKED
		} else {
			glog.Infof("%s dpll is not in spec, state is DPLL_%s, offset is out of range, state is FREERUN", d.iface, stateName(dpllStatus))
			d.state = event.PTP_FREERUN
			d.inSpec = false
			d.phaseOffset = FaultyPhaseOffset
			select {
			case d.holdoverCloseCh <- true:
				glog.Infof("closing holdover for %s since offset if out of spec", d.iface)
			default:
			}
		}
	}
	d.sendDpllEvent()
}

// sendDpllEvent sends DPLL event to the event channel
func (d *DpllConfig) sendDpllEvent() {
	if d.processConfig.EventChannel == nil {
		glog.Info("Skip event - dpll is not yet initialized")
		return
	}
	ppsStatus := 1
	if d.sourceLost {
		ppsStatus = 0
	}
	vals := map[event.ValueType]interface{}{
		event.PPS_STATUS:               int64(ppsStatus),
		event.InSyncConditionThreshold: d.inSyncConditionThreshold,
		event.InSyncConditionTimes:     d.inSyncConditionTimes,
		event.ToFreeRunThreshold:       d.LocalMaxHoldoverOffSet,
		event.MaxInSpecOffset:          d.MaxInSpecOffset,
		event.LeadingSource:            d.hasLeadingSource(),
	}
	if !d.hasFlag(FlagNoFreqencyStatus) {
		vals[event.FREQUENCY_STATUS] = d.frequencyStatus
	}
	if !d.hasFlag(FlagNoPhaseStatus) {
		vals[event.PHASE_STATUS] = d.phaseStatus
	}
	if !d.hasFlag(FlagNoPhaseOffset) {
		vals[event.OFFSET] = d.phaseOffset
	}
	eventData := event.Event{
		Source:     event.DPLL,
		IFace:      d.iface,
		CfgName:    d.processConfig.ConfigName,
		ClockType:  d.processConfig.ClockType,
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Reset:      false,
		Data: &event.PTPData{
			State:              d.state,
			OutOfSpec:          !d.inSpec,
			SourceLost:         d.sourceLost,
			FrequencyTraceable: d.frequencyTraceable,
			Values:             vals,
		},
	}
	select {
	case d.processConfig.EventChannel <- eventData:
		glog.Infof("dpll event sent for (%s): state %v, Offset %s, In spec %v, Source %v lost %v, On holdover %v",
			d.iface, d.state, d.phaseOffsetStr(), d.inSpec, d.dependsOn[0], d.sourceLost, d.onHoldover)
	default:
		glog.Infof("failed to send dpll event, retying.(%s)", d.iface)
	}
}

func (d *DpllConfig) getDpllState() int64 {
	switch {
	case d.hasPTPAsSource(), d.hasPPSAsSource():
		// 1PPS and T-BC time the PPS DPLL. EEC lock is independent and is
		// often unlocked on follower cards that take 1PPS from the leading NIC.
		return d.phaseStatus
	case d.hasFlag(FlagNoPhaseStatus):
		// Special case if there is no Phase Status (pps) for this DPLL
		return d.frequencyStatus
	case d.hasFlag(FlagNoFreqencyStatus):
		// Special case if there is no Frequency Status (eec) for this DPLL
		return d.phaseStatus
	default:
		// Normal case: Worst state of phase or frequency status
		return d.getWorseState(d.phaseStatus, d.frequencyStatus)
	}
}

// getStateQuality maps the state with relatively worse signal quality with
// a lower number for easy comparison
// Ref: ITU-T G.781 section 6.3.1 Auto selection operation
func (d *DpllConfig) getStateQuality() map[int64]float64 {
	return map[int64]float64{
		DPLL_UNKNOWN:       -1,
		DPLL_INVALID:       0,
		DPLL_FREERUN:       1,
		DPLL_HOLDOVER:      2,
		DPLL_LOCKED:        3,
		DPLL_LOCKED_HO_ACQ: 4,
	}
}

// getWorseState returns the state with worse signal quality
func (d *DpllConfig) getWorseState(pstate, fstate int64) int64 {
	sq := d.getStateQuality()
	if sq[pstate] < sq[fstate] {
		return pstate
	}
	return fstate
}

func (d *DpllConfig) holdover() {
	start := time.Now()
	ticker := time.NewTicker(1 * time.Second)
	defer func() {
		ticker.Stop()
		d.Lock()
		d.onHoldover = false
		d.sendDpllEvent()
		d.Unlock()
	}()
	d.sendDpllEvent()
	glog.Infof("setting dpll holdover for max holdover %v", d.LocalHoldoverTimeout)
	for timeout := time.After(time.Duration(int64(d.LocalHoldoverTimeout) * int64(time.Second))); ; {
		select {
		case <-ticker.C:
			d.phaseOffset = int64(math.Round((d.slope) * time.Since(start).Seconds()))
			glog.Infof("(%s) time since holdover start %f, offset %d nanosecond holdover %s", d.iface, time.Since(start).Seconds(), d.phaseOffset, strconv.FormatBool(d.onHoldover))
			if d.hasGNSSAsSource() {
				//nolint:all
				if d.frequencyTraceable {
					// TODO:  not implemented : add when syncE is handled here
					// use  !d.isInSpecOffsetInRange()  to declare HOLDOVER with  clockClass 140
					// !d.isMaxHoldoverOffsetInRange()  for clock class to move from 140 to 248 and event to FREERUN
				} else if !d.isInSpecOffsetInRange() { // when holdover verify with local max holdover not with regular threshold
					d.inSpec = false // will be in HO, Out of spec only if  frequency is traceable
					d.state = event.PTP_FREERUN
					d.phaseOffset = FaultyPhaseOffset
					d.sendDpllEvent()
					return
				}
				d.sendDpllEvent()
			} else {
				if !d.isInSpecOffsetInRange() {
					d.inSpec = false
				}
				d.state = event.PTP_HOLDOVER
				d.sendDpllEvent()
			}
		case <-timeout: // since ts2phc has same timer , ts2phc should also move out of holdover
			d.inSpec = false // not in HO, Out of spec
			d.state = event.PTP_FREERUN
			d.phaseOffset = FaultyPhaseOffset
			glog.Infof("holdover timer %d expired", d.timer)
			d.sendDpllEvent()
			return
		case <-d.holdoverCloseCh:
			glog.Info("holdover was closed")
			d.inSpec = true // if someone else is closing then it should be back in spec (if it was not in spec before)
			return
		}
	}
}

func (d *DpllConfig) isMaxHoldoverOffsetInRange() bool {
	if d.hasFlag(FlagNoPhaseOffset) {
		// Special case when the DPLL has no reported phase offset
		return true
	}
	if d.phaseOffset <= int64(d.LocalMaxHoldoverOffSet) {
		return true
	}
	glog.Infof("in holdover- dpll offset is out of range:  max %d, current %d",
		d.LocalMaxHoldoverOffSet, d.phaseOffset)
	return false
}

func (d *DpllConfig) isInSpecOffsetInRange() bool {
	if d.hasFlag(FlagNoPhaseOffset) {
		// Special case when the DPLL has no reported phase offset
		return true
	}
	if d.phaseOffset <= int64(d.MaxInSpecOffset) {
		return true
	}
	glog.Infof("in holdover- dpll inspec offset is out of range:  max %d, current %d",
		d.MaxInSpecOffset, d.phaseOffset)
	return false
}

// isOffsetInRange returns true when abs(phaseOffset) < GMThreshold.Max
// (non-inclusive boundary). GMThreshold.Min is deprecated and intentionally
// ignored here.
func (d *DpllConfig) isOffsetInRange() bool {
	if d.hasFlag(FlagNoPhaseOffset) {
		// Special case when the DPLL has no reported phase offset
		return true
	}
	if math.Abs(float64(d.phaseOffset)) < float64(d.processConfig.GMThreshold.Max) {
		return true
	}
	glog.Infof("dpll offset out of range: max %d, current %d",
		d.processConfig.GMThreshold.Max, d.phaseOffset)
	return false
}

// Index of DPLL being configured [0:EEC (DPLL0), 1:PPS (DPLL1)]
// Frequency State (EEC_DPLL)
// cat /sys/class/net/interface_name/device/dpll_0_state

func CalculateTimer(nodeProfile *ptpv1.PtpProfile) (int64, int64, int64, int64, bool) {
	var localMaxHoldoverOffSet uint64 = LocalMaxHoldoverOffSet
	var localHoldoverTimeout uint64 = LocalHoldoverTimeout
	var maxInSpecOffset uint64 = MaxInSpecOffset

	for k, v := range (*nodeProfile).PtpSettings {
		i, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		if k == LocalMaxHoldoverOffSetStr {
			localMaxHoldoverOffSet = i
		}
		if k == LocalHoldoverTimeoutStr {
			localHoldoverTimeout = i
		}
		if k == MaxInSpecOffsetStr {
			maxInSpecOffset = i
		}
	}
	slope := float64(localMaxHoldoverOffSet) / float64(localHoldoverTimeout)
	inSpecTimer := int64(math.Round(float64(maxInSpecOffset) / slope))
	return int64(maxInSpecOffset), int64(localMaxHoldoverOffSet), int64(localHoldoverTimeout), inSpecTimer, false
}

// PtpSettingsDpllIgnoreKey returns the PtpSettings key to ignore DPLL for the given interface name:
func PtpSettingsDpllIgnoreKey(iface string) string {
	return fmt.Sprintf("dpll.%s.ignore", iface)
}

// PtpSettingsDpllFlagsKey returns the PtpSettings key to set DPLL behavioral flags for the given interface name:
func PtpSettingsDpllFlagsKey(iface string) string {
	return fmt.Sprintf("dpll.%s.flags", iface)
}
