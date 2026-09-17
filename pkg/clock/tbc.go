package clock

import (
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
)

const (
	// FaultyPhaseOffset is a value assigned to the phase offset when free-running
	FaultyPhaseOffset int64 = 99999999999
	// StaleEventAfter is the number of milliseconds after which an event is considered stale
	StaleEventAfter int64 = 2000
)

// LeadingClockParams ... leading clock parameters includes state
// and configuration of the system leading clock. There is only
// one leading clock in the system. The leading clock is the clock that
// receives phase, frequency and ToD synchronization from an external source.
// Currently used for T-BC only
type LeadingClockParams struct {
	upstreamTimeProperties        *protocol.TimePropertiesDS
	upstreamParentDataSet         *protocol.ParentDataSet
	upstreamCurrentDSStepsRemoved uint16

	downstreamTimeProperties *protocol.TimePropertiesDS
	downstreamParentDataSet  *protocol.ParentDataSet

	leadingInterface         string
	controlledPortsConfig    string
	inSyncConditionThreshold int
	inSyncConditionTimes     int
	toFreeRunThreshold       int
	MaxInSpecOffset          uint64
	lastInSpec               bool
	inSyncThresholdCounter   int
	clockID                  string
}

func newLeadingClockParams() *LeadingClockParams {
	return &LeadingClockParams{
		upstreamParentDataSet:    &protocol.ParentDataSet{},
		upstreamTimeProperties:   &protocol.TimePropertiesDS{},
		downstreamParentDataSet:  &protocol.ParentDataSet{},
		downstreamTimeProperties: &protocol.TimePropertiesDS{},
	}
}

// announceSeq hands out process-unique, monotonically increasing tokens.
// Each TBC takes a fresh token at construction, on Reset(), and on every
// downstream request. A downstream-announce fetch captures the current token and
// stamps it on the result event; handleDownstreamAnnounceIWF drops any result
// whose token no longer matches the clock's current one, so a fetch that was
// superseded by a newer request, or that outlived a Reset or a clock
// replacement, cannot apply stale upstream data.
var announceSeq atomic.Uint64

func nextAnnounceToken() uint64 { return announceSeq.Add(1) }

// TBC is a Telco Boundary Clock instance.
type TBC struct {
	cfgName          string
	sendIPC          func(ipc.Message)
	sendEvent        func(event.Event)
	getUtcOffset     func() int
	pmcClient        pmc.Client
	syncState        SyncState
	overallSyncState event.PTPState
	osClockState     event.PTPState
	data             []*event.Data
	leadingClockData *LeadingClockParams
	// announceToken is a process-unique token identifying the current downstream
	// request. It advances at construction, on Reset, and on every downstream
	// request, so a fetch result carrying an out-of-date token (from a superseded
	// request, a Reset, or a replaced clock) is recognized as stale and dropped.
	// It is only read and written on the serialized state loop.
	announceToken uint64
}

// ClockType returns the clock type for this TBC clock.
func (c *TBC) ClockType() event.ClockType { return event.TBC }

// ClockClass returns the current clock class.
func (c *TBC) ClockClass() fbprotocol.ClockClass { return c.syncState.ClockClass }

// ConfigName returns the configuration name.
func (c *TBC) ConfigName() string { return c.cfgName }

// GetState returns the current PTP synchronization state.
func (c *TBC) GetState() event.PTPState { return c.syncState.State }

// GetData returns the Data entry for the given process, creating one if needed.
func (c *TBC) GetData(processName event.EventSource) *event.Data {
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
func (c *TBC) AddEvent(ev event.Event) SyncState {
	switch ev.Source {
	case event.SYNCE:
		c.processSyncE(ev)
		return c.syncState
	case event.PMC:
		ds, ok := ev.Data.(*event.ParentDSData)
		if ok {
			c.updateUpstreamParentDataSet(ds.ParentDataSet)
			c.evaluateState()
		} else if currentDS, isCurrentDS := ev.Data.(*event.ParentTimeCurrentDS); isCurrentDS {
			c.handleDownstreamAnnounceIWF(currentDS)
		}
		return c.syncState
	default:
		d := c.GetData(ev.Source)
		d.AddEvent(ev)
		d.UpdateState()
		c.updateLeadingClockData(ev)
		c.evaluateState()
		return c.syncState
	}
}

func (c *TBC) evaluateState() {
	prevState := c.syncState.State
	prevClockClass := c.syncState.ClockClass

	needsDownstreamUpdate := c.updateState()

	profile := strings.Replace(c.cfgName, "ts2phc", "ptp4l", 1)
	if c.syncState.State != prevState {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypePTPState,
			Profile: profile,
			IFace:   c.syncState.LeadingIFace,
			Values:  ipc.StateValue{State: event.PtpStateToIPCState(c.syncState.State)},
		})
	}
	if c.syncState.ClockClass != prevClockClass {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypeClockClass,
			Profile: profile,
			Values:  ipc.ClockClassValue{ClockClass: uint8(c.syncState.ClockClass)},
		})
	}

	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState.State, c.osClockState, profile)

	if needsDownstreamUpdate {
		c.updateDownstreamData(c.cfgName)
	}
}

func (c *TBC) status() string {
	return fmt.Sprintf("T-BC[%d]:[%s] %s offset %d T-BC-STATUS %s\n",
		c.syncState.LastLoggedTime, c.cfgName, c.syncState.LeadingIFace, c.syncState.ClockOffset, c.syncState.State)
}

// updateState updates the BC/TSC state machine using TBC's owned
// syncState and data. Returns whether a TTSC clock class announcement is
// needed and whether downstream data needs updating.
func (c *TBC) updateState() bool {
	dpllState := event.PTP_NOTSET
	ts2phcState := event.PTP_FREERUN
	updateDownstreamData := false
	leadingInterface := c.getLeadingInterfaceBC()
	if leadingInterface == event.LEADING_INTERFACE_UNKNOWN {
		glog.Infof("Leading interface is not yet identified, clock state reporting delayed.")
		c.syncState.LeadingIFace = leadingInterface
		return false
	}

	c.syncState.SourceLost = false
	c.syncState.LeadingIFace = leadingInterface
	if len(c.data) > 0 {
		for _, d := range c.data {
			switch d.ProcessName {
			case event.DPLL:
				dpllState = d.State
			case event.TS2PHCProcessName:
				ts2phcState = d.State
			}
		}
	} else {
		glog.Info("initializing default ClockSyncState for ", c.cfgName)
		c.syncState.State = event.PTP_FREERUN
		c.syncState.ClockClass = protocol.ClockClassFreerun
		c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
		c.syncState.LastLoggedTime = time.Now().Unix()
		c.syncState.LeadingIFace = leadingInterface
		glog.Info(c.status())
		return false
	}

	isTTSC := c.leadingClockData.clockID != "" && c.leadingClockData.controlledPortsConfig == ""

	glog.V(14).Info("current BC state: ", c.syncState.State)
	switch c.syncState.State {
	case event.PTP_NOTSET, event.PTP_FREERUN:
		if !c.isSourceLostBC() && c.inSyncCondition() {
			c.syncState.State = event.PTP_LOCKED
			glog.Info("BC FSM: FREERUN to LOCKED")
			c.leadingClockData.lastInSpec = true
			updateDownstreamData = true
		}
	case event.PTP_LOCKED:
		if c.freeRunCondition() || c.hasNonLeadingDPLLFault() {
			c.syncState.State = event.PTP_FREERUN
			c.syncState.ClockClass = protocol.ClockClassFreerun
			glog.Info("BC FSM: LOCKED to FREERUN")
			updateDownstreamData = true
		} else if c.isSourceLostBC() {
			c.syncState.State = event.PTP_HOLDOVER
			c.syncState.ClockClass = fbprotocol.ClockClass(135)
			glog.Info("BC FSM: LOCKED to HOLDOVER")
			c.leadingClockData.lastInSpec = true
			updateDownstreamData = true
		} else {
			if *c.leadingClockData.upstreamTimeProperties != *c.leadingClockData.downstreamTimeProperties {
				c.leadingClockData.downstreamTimeProperties = c.leadingClockData.upstreamTimeProperties
				updateDownstreamData = true
			}
			if *c.leadingClockData.upstreamParentDataSet != *c.leadingClockData.downstreamParentDataSet {
				c.leadingClockData.downstreamParentDataSet = c.leadingClockData.upstreamParentDataSet
				updateDownstreamData = true
			}
			if c.leadingClockData.upstreamParentDataSet.GrandmasterClockClass == uint8(protocol.ClockClassFreerun) {
				updateDownstreamData = false // Don't propagate uptream free run and instead let future call move to holdover/freerun
			} else if !isTTSC {
				upstreamClass := fbprotocol.ClockClass(c.leadingClockData.upstreamParentDataSet.GrandmasterClockClass)
				upstreamAccuracy := fbprotocol.ClockAccuracy(c.leadingClockData.upstreamParentDataSet.GrandmasterClockAccuracy)
				if c.syncState.ClockClass != upstreamClass || c.syncState.ClockAccuracy != upstreamAccuracy {
					c.syncState.ClockClass = upstreamClass
					c.syncState.ClockAccuracy = upstreamAccuracy
				}
			}
		}
	case event.PTP_HOLDOVER:
		nonLeadingFault := c.hasNonLeadingDPLLFault()
		switch {
		case nonLeadingFault || c.freeRunCondition():
			c.syncState.State = event.PTP_FREERUN
			c.syncState.ClockClass = protocol.ClockClassFreerun
			glog.Info("BC FSM: HOLDOVER to FREERUN")
			updateDownstreamData = true
		case c.inSyncCondition() && !c.isSourceLostBC():
			c.syncState.State = event.PTP_LOCKED
			glog.Info("BC FSM: HOLDOVER to LOCKED")
			updateDownstreamData = true
		default:
			inSpec := false
			if c.leadingClockData.lastInSpec {
				inSpec = c.inSpecCondition()
			}
			if c.leadingClockData.lastInSpec != inSpec {
				c.leadingClockData.lastInSpec = inSpec
				if !inSpec {
					if c.syncState.ClockClass != fbprotocol.ClockClass(165) {
						c.syncState.ClockClass = fbprotocol.ClockClass(165)
						glog.Info("BC FSM: HOLDOVER sub-state Out Of Spec")
						updateDownstreamData = true
					}
				} else {
					if c.syncState.ClockClass != fbprotocol.ClockClass(135) {
						c.syncState.ClockClass = fbprotocol.ClockClass(135)
						glog.Info("BC FSM: HOLDOVER sub-state In Spec")
						updateDownstreamData = true
					}
				}
			}
		}
	}
	c.syncState.LeadingIFace = leadingInterface
	if c.syncState.State != event.PTP_LOCKED {
		c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
	}

	switch c.syncState.State {
	case event.PTP_FREERUN:
		c.syncState.ClockOffset = FaultyPhaseOffset
	case event.PTP_HOLDOVER:
		c.syncState.ClockOffset = c.getCalculatedHoldoverOffset()
	default:
		c.syncState.ClockOffset = c.getLargestOffset()
	}

	if isTTSC && c.syncState.ClockClass != fbprotocol.ClockClassSlaveOnly {
		c.syncState.ClockClass = fbprotocol.ClockClassSlaveOnly
	}
	var needsDownstreamUpdate bool
	if updateDownstreamData && c.syncState.ClockClass != protocol.ClockClassUninitialized {
		if !isTTSC {
			needsDownstreamUpdate = true
		}
	}
	logTime := time.Now().Unix()
	if c.syncState.LastLoggedTime != logTime {
		glog.Info(c.status())
		c.syncState.LastLoggedTime = logTime
		glog.Infof("dpll State %s, tsphc state %s, BC state %s, BC offset %d",
			dpllState, ts2phcState, c.syncState.State, c.syncState.ClockOffset)
	}
	return needsDownstreamUpdate
}

// Reset resets the clock state.
func (c *TBC) Reset() {
	c.leadingClockData = newLeadingClockParams()
	c.syncState = SyncState{
		State:         event.PTP_NOTSET,
		ClockClass:    protocol.ClockClassUninitialized,
		ClockAccuracy: fbprotocol.ClockAccuracyUnknown,
	}
	c.overallSyncState = event.PTP_FREERUN
	c.data = nil
	// Advance the epoch so any downstream-announce fetch spawned before this
	// Reset is discarded when its result comes back on the loop.
	c.announceToken = nextAnnounceToken()
}

func (c *TBC) updateUpstreamParentDataSet(parentDS protocol.ParentDataSet) {
	if !parentDS.Equal(c.leadingClockData.upstreamParentDataSet) {
		c.leadingClockData.upstreamParentDataSet = &parentDS
	}
}

func (c *TBC) updateDownstreamData(cfgName string) {
	if c.syncState.State != event.PTP_LOCKED {
		c.announceLocalData(cfgName)
		return
	}
	// Advance the token so this request supersedes any downstream fetch already
	// in flight: when an older fetch's result finally lands it will carry a stale
	// token and be dropped before it touches the controlled ports. The token is
	// captured on-loop here; the goroutine never reads it.
	c.announceToken = nextAnnounceToken()
	go c.downstreamAnnounceIWF(cfgName, c.announceToken)
}

// Implements Rec. ITU-T G.8275 (2024) Amd. 1 (08/2024)
// Table VIII.3 − T-BC-/ T-BC-P/ T-BC-A Announce message contents
// for free-run (acquiring), holdover within / out of the specification
func (c *TBC) announceLocalData(cfgName string) {
	clockID := c.leadingClockData.clockID
	controlledPortsConfig := c.leadingClockData.controlledPortsConfig
	downstreamTimeProperties := c.leadingClockData.downstreamTimeProperties
	clockClass := c.syncState.ClockClass

	egp := protocol.ExternalGrandmasterProperties{
		GrandmasterIdentity: clockID,
		StepsRemoved:        0,
	}
	glog.Infof("EGP %++v", egp)
	go func() {
		if err := c.pmcClient.SetExternalGMPropertiesNP(controlledPortsConfig, egp); err != nil {
			glog.Errorf("Failed to set external GM properties: %v", err)
		}
	}()
	gs := protocol.GrandmasterSettings{
		ClockQuality: fbprotocol.ClockQuality{
			ClockClass:              clockClass,
			ClockAccuracy:           fbprotocol.ClockAccuracyUnknown,
			OffsetScaledLogVariance: 0xffff,
		},
		TimePropertiesDS: protocol.TimePropertiesDS{
			TimeSource: fbprotocol.TimeSourceInternalOscillator,
		},
	}
	switch clockClass {
	case protocol.ClockClassFreerun:
		gs.TimePropertiesDS.CurrentUtcOffsetValid = false
		gs.TimePropertiesDS.Leap59 = false
		gs.TimePropertiesDS.Leap61 = false
		gs.TimePropertiesDS.PtpTimescale = true
		gs.TimePropertiesDS.TimeTraceable = false
		gs.TimePropertiesDS.FrequencyTraceable = false
		gs.TimePropertiesDS.CurrentUtcOffset = int32(c.getUtcOffset())
	case fbprotocol.ClockClass(165), fbprotocol.ClockClass(135):
		if downstreamTimeProperties == nil {
			glog.Info("Pending upstream clock data acquisition, skip updates")
			return
		}
		gs.TimePropertiesDS.CurrentUtcOffsetValid = downstreamTimeProperties.CurrentUtcOffsetValid
		gs.TimePropertiesDS.Leap59 = downstreamTimeProperties.Leap59
		gs.TimePropertiesDS.Leap61 = downstreamTimeProperties.Leap61
		gs.TimePropertiesDS.PtpTimescale = true
		if clockClass == fbprotocol.ClockClass(135) {
			gs.TimePropertiesDS.TimeTraceable = true
		} else {
			gs.TimePropertiesDS.TimeTraceable = false
		}
		gs.TimePropertiesDS.FrequencyTraceable = false
		gs.TimePropertiesDS.CurrentUtcOffset = downstreamTimeProperties.CurrentUtcOffset

	default:
	}
	go func() {
		if err := c.pmcClient.SetGMSettings(controlledPortsConfig, gs); err != nil {
			glog.Errorf("Failed to set GM settings: %v", err)
		}
	}()
	go func() {
		if err := c.pmcClient.SetGMSettings(cfgName, gs); err != nil {
			glog.Errorf("Failed to set GM settings: %v", err)
		}
	}()
}

// downstreamAnnounceIWF fetches the upstream parent/time/current datasets via PMC and posts them back to the
// state-machine loop as an event.PMC
func (c *TBC) downstreamAnnounceIWF(cfgName string, generation uint64) {
	glog.Infof("downstreamAnnounceIWF: %s", strings.Replace(cfgName, "ts2phc", "ptp4l", 1))

	upstreamData, fetchErr := c.pmcClient.GetParentTimeAndCurrentDS(cfgName)
	if fetchErr != nil {
		glog.Error("Failed to fetch upstream data, downstream data can not be updated.")
		return
	}

	// when results are eventually received, send an event back to the queue to be handled by the state machine
	c.sendEvent(event.Event{
		Source:  event.PMC,
		CfgName: cfgName,
		Time:    time.Now().UnixMilli(),
		Data:    &event.ParentTimeCurrentDS{ParentTimeCurrentDS: upstreamData, Generation: generation},
	})
}

// handleDownstreamAnnounceIWF runs on the state-machine loop (via AddEvent). It applies the freshly fetched upstream
// datasets and propagates them to the controlled downstream ports via PMC
func (c *TBC) handleDownstreamAnnounceIWF(msg *event.ParentTimeCurrentDS) {
	if msg.Generation != c.announceToken {
		// A newer downstream request was made (or the clock was Reset/replaced)
		// while this fetch was in flight, so this result is stale: drop it before
		// it can push superseded data to the controlled ports.
		glog.Infof("downstreamAnnounceIWF: dropping stale result (token %d != current %d)", msg.Generation, c.announceToken)
		return
	}
	if c.syncState.State != event.PTP_LOCKED {
		// lock was lost in between fetching the data and handling this
		glog.Infof("downstreamAnnounceIWF: BC state is %s (not LOCKED), aborting", c.syncState.State)
		return
	}
	upstreamData := &msg.ParentTimeCurrentDS

	c.leadingClockData.upstreamParentDataSet = &upstreamData.ParentDataSet
	c.leadingClockData.upstreamTimeProperties = &upstreamData.TimePropertiesDS
	c.leadingClockData.upstreamCurrentDSStepsRemoved = upstreamData.CurrentDS.StepsRemoved

	gs := protocol.GrandmasterSettings{
		ClockQuality: fbprotocol.ClockQuality{
			ClockClass:              fbprotocol.ClockClass(upstreamData.ParentDataSet.GrandmasterClockClass),
			ClockAccuracy:           fbprotocol.ClockAccuracy(upstreamData.ParentDataSet.GrandmasterClockAccuracy),
			OffsetScaledLogVariance: upstreamData.ParentDataSet.GrandmasterOffsetScaledLogVariance,
		},
		TimePropertiesDS: upstreamData.TimePropertiesDS,
	}
	es := protocol.ExternalGrandmasterProperties{
		GrandmasterIdentity: upstreamData.ParentDataSet.GrandmasterIdentity,
		StepsRemoved:        upstreamData.CurrentDS.StepsRemoved,
	}
	controlledPortsConfig := c.leadingClockData.controlledPortsConfig
	glog.Infof("%++v", es)

	// TODO: This is synchronous for now for simplicity, to be handled in a follow-up PR
	//       Complications here revolve around out-of-order goroutine execution. May be best to use a channel (queue)
	//       with a synchronous write and a dedicated goroutine to address any potential scheduling issues.
	if err := c.pmcClient.SetExternalGMPropertiesNP(controlledPortsConfig, es); err != nil {
		glog.Error(err)
	}
	if err := c.pmcClient.SetGMSettings(controlledPortsConfig, gs); err != nil {
		glog.Error(err)
	}

	c.leadingClockData.downstreamParentDataSet = &upstreamData.ParentDataSet
	c.leadingClockData.downstreamTimeProperties = &upstreamData.TimePropertiesDS
}

// hasNonLeadingDPLLFault returns true when the leading DPLL is locked but at
// least one non-leading DPLL is not locked, indicating a follower fault that
// should force the composite clock to FREERUN.
func (c *TBC) hasNonLeadingDPLLFault() bool {
	leadingInterface := c.syncState.LeadingIFace
	if leadingInterface == event.LEADING_INTERFACE_UNKNOWN {
		return false
	}
	for _, d := range c.data {
		if d.ProcessName != event.DPLL {
			continue
		}
		leadingDetail := d.GetDataDetails(leadingInterface)
		if leadingDetail == nil || leadingDetail.State != event.PTP_LOCKED {
			return false
		}
		for _, dd := range d.Details {
			if dd.IFace != leadingInterface && dd.State != event.PTP_LOCKED {
				glog.Infof("non-leading DPLL %s is %s while leading %s is locked, composite DPLL forced to FREERUN",
					dd.IFace, dd.State, leadingInterface)
				return true
			}
		}
	}
	return false
}

func (c *TBC) inSyncCondition() bool {
	if c.leadingClockData.inSyncConditionThreshold == 0 {
		glog.Info("Leading clock in-sync condition is pending initialization")
		return false
	}

	worstOffset := c.getLargestOffset()
	if math.Abs(float64(worstOffset)) < float64(c.leadingClockData.inSyncConditionThreshold) {
		c.leadingClockData.inSyncThresholdCounter++
		if c.leadingClockData.inSyncThresholdCounter >= c.leadingClockData.inSyncConditionTimes {
			return true
		}
	} else {
		c.leadingClockData.inSyncThresholdCounter = 0
	}

	glog.Info("sync condition not reached: worst offset ", worstOffset, " count ",
		c.leadingClockData.inSyncThresholdCounter, " out of ", c.leadingClockData.inSyncConditionTimes)

	return false
}

func (c *TBC) isSourceLostBC() bool {
	ptpLost := true
	dpllLost := false
	dpllLostIface := ""
	for _, d := range c.data {
		if d.ProcessName == event.PTP4l {
			for _, dd := range d.Details {
				if dd.State == event.PTP_LOCKED {
					ptpLost = false
				}
			}
		}
		if d.ProcessName == event.DPLL {
			glog.V(14).Infof("isSourceLostBC[%s]", c.cfgName)
			for _, dd := range d.Details {
				glog.V(14).Infof("  DPLL detail: iface=%s state=%s signalSource=%s sourceLost=%t offset=%d time=%d metrics=%+v", dd.IFace, dd.State, dd.SignalSource, dd.SourceLost, dd.Offset, dd.Time, dd.Metrics)
				if dd.State != event.PTP_LOCKED {
					dpllLost = true
					dpllLostIface = dd.IFace
					break
				}
			}
		}
	}
	glog.Infof("Source %s: ptpLost %t, dpllLost %t %s",
		func() string {
			if dpllLost || ptpLost {
				return "LOST"
			}
			return "NOT LOST"
		}(), ptpLost, dpllLost, dpllLostIface)
	return ptpLost || dpllLost
}

func (c *TBC) getLargestOffset() int64 {
	worstOffset := FaultyPhaseOffset
	staleTime := time.Now().UnixMilli() - StaleEventAfter
	for _, d := range c.data {
		// ts2phc offsets are not window-filtered. On T-BC the leading NIC only
		// reports ts2phc offsets in holdover, so requiring a full sliding window
		// leaves a permanently partial window that poisons inSync with FaultyPhaseOffset.
		if d.ProcessName == event.TS2PHCProcessName {
			for _, dd := range d.Details {
				if dd.Time < staleTime {
					continue
				}
				if worstOffset == FaultyPhaseOffset ||
					math.Abs(float64(dd.Offset)) > math.Abs(float64(worstOffset)) {
					worstOffset = dd.Offset
				}
			}
			continue
		}
		if d.Window.IsEmpty() {
			continue
		}
		if !d.Window.IsFull() {
			glog.Infof("Largest offset %d (window not full for %s)", FaultyPhaseOffset, d.ProcessName)
			return FaultyPhaseOffset
		}
		for _, dd := range d.Details {
			if dd.Time < staleTime {
				continue
			}
			if worstOffset == FaultyPhaseOffset {
				if dd.IFace == c.syncState.LeadingIFace {
					worstOffset = int64(d.Window.Mean())
				} else {
					worstOffset = dd.Offset
				}
			} else {
				if math.Abs(float64(dd.Offset)) > math.Abs(float64(worstOffset)) {
					worstOffset = dd.Offset
				}
			}
		}
	}
	glog.Info("Largest offset ", worstOffset)
	return worstOffset
}

func (c *TBC) getCalculatedHoldoverOffset() int64 {
	for _, d := range c.data {
		if d.ProcessName == event.DPLL {
			return int64(d.Window.LastInserted())
		}
	}
	return FaultyPhaseOffset // No DPLL entries yet return faulty
}

func (c *TBC) freeRunCondition() bool {
	if c.leadingClockData.toFreeRunThreshold == 0 {
		glog.Info("Leading clock free-run condition is pending initialization")
		return true
	}
	for _, d := range c.data {
		switch d.ProcessName {
		case event.DPLL:
			for _, dd := range d.Details {
				if dd.IFace == c.syncState.LeadingIFace {
					if math.Abs(float64(dd.Offset)) > float64(c.leadingClockData.toFreeRunThreshold) {
						glog.Infof("free-run condition on DPLL %s", dd.IFace)
						return true
					}
				}
			}
		case event.PTP4l:
			if d.Window.IsEmpty() {
				continue
			}
			ptp4lAvgOffset := int64(d.Window.Mean())
			if math.Abs(float64(ptp4lAvgOffset)) > float64(c.leadingClockData.toFreeRunThreshold) {
				glog.Infof("free-run condition on PTP4l, avg offset %d", ptp4lAvgOffset)
				return true
			}
		}
	}
	return false
}

func (c *TBC) inSpecCondition() bool {
	if c.leadingClockData.MaxInSpecOffset == 0 {
		glog.Info("Leading clock in-spec condition is pending initialization")
		return false
	}
	for _, d := range c.data {
		if d.ProcessName == event.DPLL {
			for _, dd := range d.Details {
				if dd.IFace == c.syncState.LeadingIFace {
					if math.Abs(float64(dd.Offset)) > float64(c.leadingClockData.MaxInSpecOffset) {
						glog.Infof("out-of-spec condition on DPLL ", dd.IFace)
						return false
					}
				}
			}
		}
	}
	return true
}

func (c *TBC) getLeadingInterfaceBC() string {
	if c.leadingClockData.leadingInterface != "" {
		return c.leadingClockData.leadingInterface
	}
	return event.LEADING_INTERFACE_UNKNOWN
}

// SystemClockUpdate updates the OS clock state.
func (c *TBC) SystemClockUpdate(osClockState event.PTPState) {
	c.osClockState = osClockState
	profile := strings.Replace(c.cfgName, "ts2phc", "ptp4l", 1)
	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState.State, c.osClockState, profile)
}

func (c *TBC) processSyncE(ev event.Event) {
	ptp, ok := ev.Data.(*event.PTPData)
	if !ok || ptp == nil {
		return
	}
	profile := strings.Replace(c.cfgName, "ts2phc", "ptp4l", 1)

	eecState, hasEEC := ptp.Values[event.EEC_STATE].(string)
	if hasEEC {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypeSyncEState,
			Profile: profile,
			IFace:   ev.IFace,
			Values:  ipc.SyncEStateValue{State: eecState},
		})
	}

	ql, hasQL := ptp.Values[event.QL].(byte)
	extQL, hasExtQL := ptp.Values[event.EXT_QL].(byte)
	if hasQL || hasExtQL {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypeSyncEClockQuality,
			Profile: profile,
			IFace:   ev.IFace,
			Values:  ipc.SyncEClockQualityValue{QL: int(ql), ExtendedQL: int(extQL)},
		})
	}
}

func (c *TBC) updateLeadingClockData(ev event.Event) {
	ptp, ok := ev.Data.(*event.PTPData)
	if !ok {
		return
	}
	switch ev.Source {
	case event.PTP4lProcessName:
		cpc, found := ptp.Values[event.ControlledPortsConfig].(string)
		if found {
			c.leadingClockData.controlledPortsConfig = cpc
		}
		id, found := ptp.Values[event.ClockIDKey].(string)
		if found {
			c.leadingClockData.clockID = id
		}
	case event.DPLL:
		ls, found := ptp.Values[event.LeadingSource].(bool)
		if found && ls {
			c.leadingClockData.leadingInterface = ev.IFace
		}
		inSyncTh, found := ptp.Values[event.InSyncConditionThreshold].(uint64)
		if found {
			c.leadingClockData.inSyncConditionThreshold = int(inSyncTh)
		}
		inSyncTimes, found := ptp.Values[event.InSyncConditionTimes].(uint64)
		if found {
			c.leadingClockData.inSyncConditionTimes = int(inSyncTimes)
		}
		toFreeRunTh, found := ptp.Values[event.ToFreeRunThreshold].(uint64)
		if found {
			c.leadingClockData.toFreeRunThreshold = int(toFreeRunTh)
		}
		maxInSpec, found := ptp.Values[event.MaxInSpecOffset].(uint64)
		if found {
			c.leadingClockData.MaxInSpecOffset = maxInSpec
		}
	}
}
