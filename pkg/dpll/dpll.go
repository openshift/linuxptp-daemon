package dpll

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	nl "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
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

	LocalMaxHoldoverOffSet = 1500  //ns
	LocalHoldoverTimeout   = 14400 //secs
	MaxInSpecOffset        = 1500  //ns
	monitoringInterval     = 1 * time.Second

	LocalMaxHoldoverOffSetStr = "LocalMaxHoldoverOffSet"
	LocalHoldoverTimeoutStr   = "LocalHoldoverTimeout"
	MaxInSpecOffsetStr        = "MaxInSpecOffset"
	ClockIdStr                = "clockId"
	FaultyPhaseOffset         = 99999999999

	// The PPS pin index in the pin-parent-device structure
	PPS_PIN_INDEX = 1
)

type dpllApiType string

var MockDpllReplies chan *nl.DoDeviceGetReply

const (
	SYSFS   dpllApiType = "sysfs"
	NETLINK dpllApiType = "netlink"
	MOCK    dpllApiType = "mock"
	NONE    dpllApiType = "none"
)

type DependingStates struct {
	sync.Mutex
	states       map[event.EventSource]event.PTPState
	currentState event.PTPState
}

// DpllSubscriber ... event subscriber
type DpllSubscriber struct {
	source event.EventSource
	dpll   *DpllConfig
	id     string
}

var dependingProcessStateMap = DependingStates{
	states: make(map[event.EventSource]event.PTPState),
}

// GetCurrentState ... get current state
func (d *DependingStates) GetCurrentState() event.PTPState {
	return d.currentState
}

func (d *DependingStates) UpdateState(source event.EventSource) {
	// do not lock here, since this function is called from other locks
	lowestState := event.PTP_FREERUN
	for _, state := range d.states {
		if state < lowestState {
			lowestState = state
		}
	}
	d.currentState = lowestState
}

// DpllConfig ... DPLL configuration
type DpllConfig struct {
	LocalMaxHoldoverOffSet uint64
	LocalHoldoverTimeout   uint64
	MaxInSpecOffset        uint64
	iface                  string
	name                   string
	slope                  float64
	timer                  int64 //secs
	inSpec                 bool
	frequencyTraceable     bool
	state                  event.PTPState
	onHoldover             bool
	closing                bool
	sourceLost             bool
	processConfig          config.ProcessConfig
	dependsOn              []event.EventSource
	exitCh                 chan struct{}
	holdoverCloseCh        chan bool
	ticker                 *time.Ticker
	apiType                dpllApiType
	// DPLL netlink connection pointer. If 'nil', use sysfs
	conn *nl.Conn
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
	sync.Mutex
	isMonitoring             bool
	subscriber               []*DpllSubscriber
	phaseOffsetPinFilter     map[string]map[string]string
	inSyncConditionThreshold uint64
	inSyncConditionTimes     uint64
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

// State ... get dpll state
func (d *DpllConfig) State() event.PTPState {
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

// PhaseOffset ... get phase offset
func (d *DpllConfig) PhaseOffset() int64 {
	return d.phaseOffset
}

// FrequencyStatus ... get frequency status
func (d *DpllConfig) FrequencyStatus() int64 {
	return d.frequencyStatus
}

// PhaseStatus get phase status
func (d *DpllConfig) PhaseStatus() int64 {
	return d.phaseStatus
}

// Monitor ...
func (s DpllSubscriber) Monitor() {
	glog.Infof("Starting dpll monitoring %s", s.id)
	s.dpll.MonitorDpll()
}

// Topic ... event topic
func (s DpllSubscriber) Topic() event.EventSource {
	return s.source
}

func (s DpllSubscriber) ID() string {
	return s.id
}

// Notify ... event notification
func (s DpllSubscriber) Notify(source event.EventSource, state event.PTPState) {
	if s.dpll == nil || !s.dpll.isMonitoring {
		glog.Errorf("dpll subscriber %s is not initialized (monitoring state %t)", s.source, s.dpll.isMonitoring)
		return
	}
	dependingProcessStateMap.Lock()
	defer dependingProcessStateMap.Unlock()
	currentState := dependingProcessStateMap.states[source]
	if currentState != state {
		glog.Infof("%s notified on state change: from state %v to state %v", source, currentState, state)
		dependingProcessStateMap.states[source] = state
		if source == event.GNSS {
			if state == event.PTP_LOCKED {
				s.dpll.sourceLost = false
			} else {
				s.dpll.sourceLost = true
			}
			glog.Infof("sourceLost %v", s.dpll.sourceLost)
		}
		s.dpll.stateDecision()
		glog.Infof("%s notified on state change: state %v", source, state)
		dependingProcessStateMap.UpdateState(source)
	}
}

// Name ... name of the process
func (d *DpllConfig) Name() string {
	return string(event.DPLL)
}

// Stopped ... stopped
func (d *DpllConfig) Stopped() bool {
	//TODO implement me
	panic("implement me")
}

// ExitCh ... exit channel
func (d *DpllConfig) ExitCh() chan struct{} {
	return d.exitCh
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

// CmdStop ... stop command
func (d *DpllConfig) CmdStop() {
	glog.Infof("stopping %s", d.Name())
	d.ticker.Stop()
	glog.Infof("Ticker stopped %s", d.Name())
	close(d.exitCh) // terminate loop
	glog.Infof("Process %s terminated", d.Name())
}

// CmdInit ... init command
func (d *DpllConfig) CmdInit() {
	// register to event notification from other processes
	if d.apiType != MOCK { // use mock type unit test DPLL
		d.setAPIType()
	}
	glog.Infof("api type %v", d.apiType)
}

// ProcessStatus ... process status
func (d *DpllConfig) ProcessStatus(_ net.Conn, _ int64) {
}

// CmdRun ... run command
func (d *DpllConfig) CmdRun(stdToSocket bool) {
	// noting to run, monitor() function takes care of dpll run
}

func (d *DpllConfig) unRegisterAll() {
	// register to event notification from other processes
	for _, s := range d.subscriber {
		event.StateRegisterer.Unregister(s)
	}
}

// NewDpll ... create new DPLL process
func NewDpll(clockId uint64, localMaxHoldoverOffSet, localHoldoverTimeout, maxInSpecOffset uint64,
	iface string, dependsOn []event.EventSource, apiType dpllApiType, phaseOffsetPinFilter map[string]map[string]string,
	inSyncConditionTh uint64, inSyncConditionTimes uint64) *DpllConfig {
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
		exitCh:                   make(chan struct{}),
		ticker:                   time.NewTicker(monitoringInterval),
		isMonitoring:             false,
		apiType:                  apiType,
		phaseOffsetPinFilter:     phaseOffsetPinFilter,
		phaseOffset:              FaultyPhaseOffset,
		inSyncConditionThreshold: inSyncConditionTh,
		inSyncConditionTimes:     inSyncConditionTimes,
	}

	// time to reach maxnInSpecOffset
	d.timer = int64(math.Round(float64(d.MaxInSpecOffset) / d.slope))
	glog.Infof("slope %f ns/s, in spec offset %f ns, in spec timer %d /sec Max timer %d /s",
		d.slope, float64(d.MaxInSpecOffset), d.timer, int64(d.LocalHoldoverTimeout))
	return d
}
func (d *DpllConfig) Slope() float64 {
	return d.slope
}

func (d *DpllConfig) Timer() int64 {
	return d.timer
}

func (d *DpllConfig) PhaseOffsetPin(pin *nl.PinInfo) bool {

	if pin.ClockID == d.clockId && len(pin.ParentDevice) > PPS_PIN_INDEX && pin.ParentDevice[PPS_PIN_INDEX].PhaseOffset != math.MaxInt64 {
		for k, v := range d.phaseOffsetPinFilter[strconv.FormatUint(d.clockId, 10)] {
			switch k {
			case "boardLabel":
				if strings.Compare(pin.BoardLabel, v) != 0 {
					return false
				}
			case "panelLabel":
				if strings.Compare(pin.PanelLabel, v) != 0 {
					return false
				}
			default:
				glog.Warningf("unsupported phase offset pin filter key: %s", k)
			}
		}
		return true
	}
	return false
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
			switch nl.GetDpllType(reply.Type) {
			case "eec":
				d.frequencyStatus = int64(reply.LockStatus)
				valid = true
			case "pps":
				d.phaseStatus = int64(reply.LockStatus)
				valid = true
			}
		}
	}
	for _, pin := range pins {
		if d.PhaseOffsetPin(pin) {
			d.SetPhaseOffset(pin.ParentDevice[PPS_PIN_INDEX].PhaseOffset)
			glog.Info("setting phase offset to ", d.phaseOffset, " ns for clock id ", d.clockId, " iface ", d.iface)
			valid = true
		}
	}
	return valid
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
		if d.nlUpdateState(devices, pins) {
			d.stateDecision()
		}
	}
}

// checks whether sysfs file structure exists for dpll associated with the interface
func (d *DpllConfig) isSysFsPresent() bool {
	path := fmt.Sprintf("/sys/class/net/%s/device/dpll_0_state", d.iface)
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

// MonitorDpllSysfs monitors DPLL through sysfs
func (d *DpllConfig) isNetLinkPresent() bool {
	conn, err := nl.Dial(nil)
	if err != nil {
		glog.Infof("failed to establish dpll netlink connection (%s): %s", d.iface, err)
		return false
	}
	conn.Close()
	return true
}

// setAPIType
func (d *DpllConfig) setAPIType() {
	if d.isSysFsPresent() {
		d.apiType = SYSFS
	} else if d.isNetLinkPresent() {
		d.apiType = NETLINK
	} else {
		d.apiType = NONE
	}
}

func (d *DpllConfig) MonitorDpllMock() {
	glog.Info("starting dpll mock monitoring")

	if d.nlUpdateState([]*nl.DoDeviceGetReply{<-MockDpllReplies}, []*nl.PinInfo{}) {
		d.stateDecision()
	}

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

			if d.nlUpdateState(replies, []*nl.PinInfo{}) {
				d.stateDecision()
			}

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
		case <-d.exitCh:
			glog.Infof("terminating netlink dpll monitoring")
			select {
			case d.processConfig.EventChannel <- event.EventChannel{
				ProcessName: event.DPLL,
				IFace:       d.iface,
				CfgName:     d.processConfig.ConfigName,
				ClockType:   d.processConfig.ClockType,
				Time:        time.Now().UnixMilli(),
				Reset:       true,
			}:
			default:
				glog.Error("failed to send dpll event terminated event")
			}
			// unregister from event notification from other processes
			d.unRegisterAllSubscriber()

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

// MonitorProcess is initiating monitoring of DPLL associated with a process
func (d *DpllConfig) MonitorProcess(processCfg config.ProcessConfig) {
	d.processConfig = processCfg
	// register to event notification from other processes
	for _, dep := range d.dependsOn {
		if dep == event.GNSS { //TODO: fow now no subscription for pps
			dependingProcessStateMap.states[dep] = event.PTP_UNKNOWN
			// register to event notification from other processes
			d.subscriber = append(d.subscriber, &DpllSubscriber{source: dep, dpll: d, id: fmt.Sprintf("%s-%x", event.DPLL, d.clockId)})
		}
	}
	// register monitoring process to be called by event
	d.subscriber = append(d.subscriber, &DpllSubscriber{
		source: event.MONITORING,
		dpll:   d,
		id:     fmt.Sprintf("%s-%x", event.DPLL, d.clockId),
	})
	d.registerAllSubscriber()
}

func (d *DpllConfig) unRegisterAllSubscriber() {
	for _, s := range d.subscriber {
		event.StateRegisterer.Unregister(s)
	}
	d.subscriber = []*DpllSubscriber{}
}

func (d *DpllConfig) registerAllSubscriber() {
	for _, s := range d.subscriber {
		event.StateRegisterer.Register(s)
	}
}

// MonitorDpll monitors DPLL on the discovered API, if any
func (d *DpllConfig) MonitorDpll() {
	fmt.Println(d.apiType)
	if d.apiType == MOCK {
		return
	} else if d.apiType == SYSFS {
		go d.MonitorDpllSysfs()
		d.isMonitoring = true
	} else if d.apiType == NETLINK {
		go d.MonitorDpllNetlink()
		d.isMonitoring = true
	} else {
		glog.Errorf("dpll monitoring is not possible, both sysfs is not available or netlink implementation is not present")
		return
	}
}

// stateDecision
func (d *DpllConfig) stateDecision() {
	var dpllStatus int64
	switch {
	case d.hasPTPAsSource():
		// For T-BC EEC DPLL state is not taken into account
		dpllStatus = d.phaseStatus
	default:
		dpllStatus = d.getWorseState(d.phaseStatus, d.frequencyStatus)
	}

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
		case d.hasPPSAsSource():
			d.state = event.PTP_FREERUN
			d.phaseOffset = FaultyPhaseOffset
			glog.Infof("Follower card DPLL is on holdover - hardware failure or pins misconfigured")
		case d.hasGNSSAsSource():
			if !d.inSpec { // d.inSpec is updated by the holdover routine
				glog.Infof("dpll is not in spec, state is DPLL_HOLDOVER, offset is out of range, state is FREERUN(%s)", d.iface)
				d.state = event.PTP_FREERUN
				d.phaseOffset = FaultyPhaseOffset
				select {
				case d.holdoverCloseCh <- true:
					glog.Infof("closing holdover for %s since offset if out of spec", d.iface)
				default:
				}
			} else if !d.onHoldover {
				d.holdoverCloseCh = make(chan bool)
				d.onHoldover = true
				d.state = event.PTP_HOLDOVER
				glog.Infof("starting holdover (%s)", d.iface)
				go d.holdover()
			}
		case d.hasPTPAsSource():
			if d.PhaseOffset() > LocalMaxHoldoverOffSet {
				glog.Infof("dpll offset is above MaxHoldoverOffSet, state is FREERUN(%s)", d.iface)
				d.state = event.PTP_FREERUN
				d.phaseOffset = FaultyPhaseOffset
				d.sourceLost = true
				select {
				case d.holdoverCloseCh <- true:
					glog.Infof("closing holdover for %s since offset if above MaxHoldoverOffSet", d.iface)
				default:
				}
			} else if !d.onHoldover && !d.closing {
				d.holdoverCloseCh = make(chan bool)
				d.onHoldover = true
				d.state = event.PTP_HOLDOVER
				glog.Infof("starting holdover (%s)", d.iface)
				go d.holdover()
			}
		}

	case DPLL_LOCKED_HO_ACQ:
		if d.isOffsetInRange() {
			d.state = event.PTP_LOCKED
			d.inSpec = true
		} else {
			d.state = event.PTP_FREERUN
			d.sourceLost = false // phase offset will be the one that was read
		}
		if d.isOffsetInRange() {
			glog.Infof("%s dpll is locked, source is not lost, offset is in range, state is DPLL_LOCKED_HO_ACQ", d.iface)
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
			glog.Infof("%s dpll is not in spec, state is DPLL_LOCKED_HO_ACQ, offset is out of range, state is FREERUN", d.iface)
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
	eventData := event.EventChannel{
		ProcessName: event.DPLL,
		State:       d.state,
		IFace:       d.iface,
		CfgName:     d.processConfig.ConfigName,
		Values: map[event.ValueType]interface{}{
			event.FREQUENCY_STATUS: d.frequencyStatus,
			event.OFFSET:           d.phaseOffset,
			event.PHASE_STATUS:     d.phaseStatus,
			event.PPS_STATUS: func() int {
				if d.sourceLost {
					return 0
				}
				return 1
			}(),
			event.LeadingSource:            d.hasLeadingSource(),
			event.InSyncConditionThreshold: d.inSyncConditionThreshold,
			event.InSyncConditionTimes:     d.inSyncConditionTimes,
			event.ToFreeRunThreshold:       d.LocalMaxHoldoverOffSet,
			event.MaxInSpecOffset:          d.MaxInSpecOffset,
		},
		ClockType:          d.processConfig.ClockType,
		Time:               time.Now().UnixMilli(),
		OutOfSpec:          !d.inSpec,
		SourceLost:         d.sourceLost, // Here source lost is either GNSS or PPS , nmea string lost is captured by ts2phc
		FrequencyTraceable: d.frequencyTraceable,
		WriteToLog:         true,
		Reset:              false,
	}
	select {
	case d.processConfig.EventChannel <- eventData:
		glog.Infof("dpll event sent for (%s): state %v, Offset %d, In spec %v, Source %v lost %v, On holdover %v",
			d.iface, d.state, d.phaseOffset, d.inSpec, d.dependsOn[0], d.sourceLost, d.onHoldover)
	default:
		glog.Infof("failed to send dpll event, retying.(%s)", d.iface)
	}
}

// MonitorDpllSysfs ... monitor dpPPS_STATUSll events
func (d *DpllConfig) MonitorDpllSysfs() {
	defer func() {
		if r := recover(); r != nil {
			glog.Warning("Recovered from panic from Monitor DPLL: ", r)
			// Handle the closed channel panic if necessary
		}
	}()

	d.ticker = time.NewTicker(monitoringInterval)

	// Determine DPLL state
	d.inSpec = true

	for {
		select {
		case <-d.exitCh:
			glog.Infof("Terminating sysfs DPLL monitoring")
			d.sendDpllTerminationEvent()

			if d.onHoldover {
				close(d.holdoverCloseCh) // Cancel any holdover
			}
			return
		case <-d.ticker.C:
			// Monitor DPLL
			d.phaseStatus, d.frequencyStatus, d.phaseOffset = d.sysfs(d.iface)
			d.stateDecision()
		}
	}
}

// sendDpllTerminationEvent sends a termination event to the event channel
func (d *DpllConfig) sendDpllTerminationEvent() {
	select {
	case d.processConfig.EventChannel <- event.EventChannel{
		ProcessName: event.DPLL,
		IFace:       d.iface,
		CfgName:     d.processConfig.ConfigName,
		ClockType:   d.processConfig.ClockType,
		Time:        time.Now().UnixMilli(),
		Reset:       true,
	}:
	default:
		glog.Error("failed to send dpll terminated event")
	}

	// unregister from event notification from other processes
	d.unRegisterAllSubscriber()
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
		d.onHoldover = false
		d.stateDecision()
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
					//TODO:  not implemented : add when syncE is handled here
					// use  !d.isInSpecOffsetInRange()  to declare HOLDOVER with  clockClass 140
					// !d.isMaxHoldoverOffsetInRange()  for clock class to move from 140 to 248 and event to FREERUN
				} else if !d.isInSpecOffsetInRange() { // when holdover verify with local max holdover not with regular threshold
					d.inSpec = false // will be in HO, Out of spec only if  frequency is traceable
					d.state = event.PTP_FREERUN
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
	if d.phaseOffset <= int64(d.LocalMaxHoldoverOffSet) {
		return true
	}
	glog.Infof("in holdover- dpll offset is out of range:  max %d, current %d",
		d.LocalMaxHoldoverOffSet, d.phaseOffset)
	return false
}
func (d *DpllConfig) isInSpecOffsetInRange() bool {
	if d.phaseOffset <= int64(d.MaxInSpecOffset) {
		return true
	}
	glog.Infof("in holdover- dpll inspec offset is out of range:  max %d, current %d",
		d.MaxInSpecOffset, d.phaseOffset)
	return false
}

func (d *DpllConfig) isOffsetInRange() bool {
	if d.phaseOffset <= d.processConfig.GMThreshold.Max && d.phaseOffset >= d.processConfig.GMThreshold.Min {
		return true
	}
	glog.Infof("dpll offset out of range: min %d, max %d, current %d",
		d.processConfig.GMThreshold.Min, d.processConfig.GMThreshold.Max, d.phaseOffset)
	return false
}

// Index of DPLL being configured [0:EEC (DPLL0), 1:PPS (DPLL1)]
// Frequency State (EEC_DPLL)
// cat /sys/class/net/interface_name/device/dpll_0_state
// Phase State
// cat /sys/class/net/ens7f0/device/dpll_1_state
// Phase Offset
// cat /sys/class/net/ens7f0/device/dpll_1_offset
func (d *DpllConfig) sysfs(iface string) (phaseState, frequencyState, phaseOffset int64) {
	if iface == "" {
		return DPLL_INVALID, DPLL_INVALID, 0
	}

	readInt64FromFile := func(path string) (int64, error) {
		content, err := os.ReadFile(path)
		if err != nil {
			return 0, err
		}
		contentStr := strings.TrimSpace(string(content))
		value, err := strconv.ParseInt(contentStr, 10, 64)
		if err != nil {
			return 0, err
		}
		return value, nil
	}

	frequencyStateStr := fmt.Sprintf("/sys/class/net/%s/device/dpll_0_state", iface)
	phaseStateStr := fmt.Sprintf("/sys/class/net/%s/device/dpll_1_state", iface)
	phaseOffsetStr := fmt.Sprintf("/sys/class/net/%s/device/dpll_1_offset", iface)

	frequencyState, err := readInt64FromFile(frequencyStateStr)
	if err != nil {
		glog.Errorf("Error reading frequency state from %s: %v", frequencyStateStr, err)
	}

	phaseState, err = readInt64FromFile(phaseStateStr)
	if err != nil {
		glog.Errorf("Error reading phase state from %s: %v %s", phaseStateStr, err, d.iface)
	}

	phaseOffset, err = readInt64FromFile(phaseOffsetStr)
	if err != nil {
		glog.Errorf("Error reading phase offset from %s: %v %s", phaseOffsetStr, err, d.iface)
	} else {
		phaseOffset /= 100 // Convert to nanoseconds from tens of picoseconds (divide by 100)
	}
	return phaseState, frequencyState, phaseOffset
}

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
