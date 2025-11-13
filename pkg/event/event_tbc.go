package event

import (
	"fmt"
	"math"
	"net"
	"strings"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
)

const (
	// LeadingSource is a key for passing the leading source
	LeadingSource ValueType = "LeadingSource"
	// InSyncConditionThreshold is a key for passing the in-sync condition threshold
	InSyncConditionThreshold ValueType = "in-sync-th"
	// InSyncConditionTimes is a key for passing the in-sync condition counter maximum
	InSyncConditionTimes ValueType = "in-sync-times"
	// ToFreeRunThreshold is a key for passing the threshold for the to-free-run condition
	ToFreeRunThreshold ValueType = "free-run_th"
	// ControlledPortsConfig is a key for passing the controlled ports config file name
	// to the controlling instance,
	ControlledPortsConfig ValueType = "controlled-ports-config"
	// ParentDataSet is a key for passing the ParentDS
	ParentDataSet ValueType = "parent-ds"
	// CurrentDataSet is a key for passing the CurrentDS
	CurrentDataSet ValueType = "current-ds"
	// ClockIDKey is a key for passing the clock ID
	ClockIDKey ValueType = "clock-id"
	//TimePropertiesDataSet is a key for passing the TimePropertiesDS
	TimePropertiesDataSet ValueType = "time-props"
	// MaxInSpecOffset is the key for passing the MaxInSpecOffset
	MaxInSpecOffset ValueType = "max-in-spec"
	// FaultyPhaseOffset is a value assigned to the phase offset when free-running
	FaultyPhaseOffset int64 = 99999999999
	// StaleEventAfter is the number of seconds after which an event is considered stale
	StaleEventAfter int64 = 2
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

func (e *EventHandler) updateBCState(event EventChannel, c net.Conn) clockSyncState {
	cfgName := event.CfgName
	dpllState := PTP_NOTSET
	ts2phcState := PTP_FREERUN
	// For internal data announces, only update the downstream data on class change
	// For External GM data announces in the locked state, update whenever any of the
	// information elements change
	updateDownstreamData := false
	leadingTS2phcActive := false

	leadingInterface := e.getLeadingInterfaceBC()
	if leadingInterface == LEADING_INTERFACE_UNKNOWN {
		glog.Infof("Leading interface is not yet identified, clock state reporting delayed.")
		return clockSyncState{leadingIFace: leadingInterface}
	}

	if _, ok := e.clkSyncState[cfgName]; !ok {
		glog.Info("initializing e.clkSyncState for ", cfgName)
		e.clkSyncState[cfgName] = &clockSyncState{
			state:         PTP_FREERUN,
			clockClass:    protocol.ClockClassUninitialized,
			clockAccuracy: fbprotocol.ClockAccuracyUnknown,
			sourceLost:    false,
			leadingIFace:  leadingInterface,
		}
	}

	e.clkSyncState[cfgName].sourceLost = false
	e.clkSyncState[cfgName].leadingIFace = leadingInterface
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			switch d.ProcessName {
			case DPLL:
				dpllState = d.State
			case TS2PHCProcessName:
				if event.IFace == leadingInterface {
					leadingTS2phcActive = true
				}
				ts2phcState = d.State
			case PTP4lProcessName:

				if leadingTS2phcActive && event.IFace == leadingInterface {
					// In T-BC configuration, leading card PHC is either updated by ts2phc, or by ptp4l
					// During holdover, when ts2phc is active, ptp4l is not updating the PHC
					// During the normal operation, ptp4l is updating the PHC, and ts2phc events stop
					// However, the data with the processName "ts2phc" is still present in the data map
					// If taken into account, it will contribute an outdated information into the decision making
					// The construct below detects the transition from ts2phc to ptp4l and invalidates the ts2phc data
					for _, tsphcData := range data {
						if tsphcData.ProcessName == TS2PHCProcessName {
							for _, tsphcDetail := range tsphcData.Details {
								if tsphcDetail.time < time.Now().Unix()-StaleEventAfter {
									tsphcDetail.Offset = 0
									leadingTS2phcActive = false
								}
							}
						}
					}
				}
			}
		}
	} else {
		glog.Info("initializing default e.clkSyncState for ", cfgName)
		e.clkSyncState[cfgName].state = PTP_FREERUN
		e.clkSyncState[cfgName].clockClass = protocol.ClockClassFreerun
		e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyUnknown
		e.clkSyncState[cfgName].lastLoggedTime = time.Now().Unix()
		e.clkSyncState[cfgName].leadingIFace = leadingInterface
		e.clkSyncState[cfgName].clkLog = fmt.Sprintf("T-BC[%d]:[%s] %s offset %d T-BC-STATUS %s\n",
			e.clkSyncState[cfgName].lastLoggedTime, cfgName, leadingInterface, e.clkSyncState[cfgName].clockOffset,
			e.clkSyncState[cfgName].state)
		return *e.clkSyncState[cfgName]
	}
	glog.Info("current BC state: ", e.clkSyncState[cfgName].state)
	switch e.clkSyncState[cfgName].state {
	case PTP_NOTSET, PTP_FREERUN:
		if !e.isSourceLostBC(cfgName) && e.inSyncCondition(cfgName) {
			e.clkSyncState[cfgName].state = PTP_LOCKED
			glog.Info("BC FSM: FREERUN to LOCKED")
			e.LeadingClockData.lastInSpec = true
			updateDownstreamData = true
		}
	case PTP_LOCKED:
		if e.freeRunCondition(cfgName) {
			e.clkSyncState[cfgName].state = PTP_FREERUN
			e.clkSyncState[cfgName].clockClass = protocol.ClockClassFreerun
			glog.Info("BC FSM: LOCKED to FREERUN")
			updateDownstreamData = true
		} else if e.isSourceLostBC(cfgName) {
			e.clkSyncState[cfgName].state = PTP_HOLDOVER
			e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass(135)
			glog.Info("BC FSM: LOCKED to HOLDOVER")
			e.LeadingClockData.lastInSpec = true
			updateDownstreamData = true
		} else {
			if *e.LeadingClockData.upstreamTimeProperties != *e.LeadingClockData.downstreamTimeProperties {
				e.LeadingClockData.downstreamTimeProperties = e.LeadingClockData.upstreamTimeProperties
				updateDownstreamData = true
			}
			if *e.LeadingClockData.upstreamParentDataSet != *e.LeadingClockData.downstreamParentDataSet {
				e.LeadingClockData.downstreamParentDataSet = e.LeadingClockData.upstreamParentDataSet
				updateDownstreamData = true
			}
		}
	case PTP_HOLDOVER:
		if e.inSyncCondition(cfgName) && !e.isSourceLostBC(cfgName) {
			e.clkSyncState[cfgName].state = PTP_LOCKED
			glog.Info("BC FSM: HOLDOVER to LOCKED")
			updateDownstreamData = true
		} else if e.freeRunCondition(cfgName) {
			e.clkSyncState[cfgName].state = PTP_FREERUN
			e.clkSyncState[cfgName].clockClass = protocol.ClockClassFreerun
			glog.Info("BC FSM: HOLDOVER to FREERUN")
			updateDownstreamData = true
		} else {
			if event.IFace == leadingInterface {
				inSpec := false
				if e.LeadingClockData.lastInSpec {
					inSpec = e.inSpecCondition(cfgName)
				}
				if e.LeadingClockData.lastInSpec != inSpec {
					e.LeadingClockData.lastInSpec = inSpec
					if !inSpec {
						if e.clkSyncState[cfgName].clockClass != fbprotocol.ClockClass(165) {
							e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass(165)
							glog.Info("BC FSM: HOLDOVER sub-state Out Of Spec")
							updateDownstreamData = true
						}
					} else {
						if e.clkSyncState[cfgName].clockClass != fbprotocol.ClockClass(135) {
							e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass(135)
							glog.Info("BC FSM: HOLDOVER sub-state In Spec")
							updateDownstreamData = true
						}
					}
				}
			}
		}
	}
	e.clkSyncState[cfgName].leadingIFace = leadingInterface
	e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyUnknown

	gSycState := e.clkSyncState[cfgName]
	rclockSyncState := clockSyncState{
		state:         gSycState.state,
		clockClass:    gSycState.clockClass,
		clockAccuracy: gSycState.clockAccuracy,
		sourceLost:    gSycState.sourceLost,
		leadingIFace:  gSycState.leadingIFace,
	}

	if gSycState.state == PTP_FREERUN {
		e.clkSyncState[cfgName].clockOffset = FaultyPhaseOffset
	} else {
		e.clkSyncState[cfgName].clockOffset = e.getLargestOffset(cfgName)
	}

	if updateDownstreamData {
		go e.updateDownstreamData(cfgName, c)
	}
	// this will reduce log noise and prints 1 per sec
	logTime := time.Now().Unix()
	if e.clkSyncState[cfgName].lastLoggedTime != logTime {
		clkLog := fmt.Sprintf("T-BC[%d]:[%s] %s offset %d T-BC-STATUS %s\n",
			logTime, cfgName, gSycState.leadingIFace, e.clkSyncState[cfgName].clockOffset, gSycState.state)
		e.clkSyncState[cfgName].lastLoggedTime = logTime
		e.clkSyncState[cfgName].clkLog = clkLog
		rclockSyncState.clkLog = clkLog
		glog.Infof("dpll State %s, tsphc state %s, BC state %s, BC offset %d",
			dpllState, ts2phcState, e.clkSyncState[cfgName].state, e.clkSyncState[cfgName].clockOffset)
	}
	return rclockSyncState
}

// UpdateUpstreamData updates the upstream time properties, parent data set, and current data set
// for the leading clock and triggers downstream data updates when changes are detected.
func (e *EventHandler) UpdateUpstreamData(cfgName string, c net.Conn, data pmc.ParentTimeCurrentDS) {
	updateDownstream := false
	if *e.LeadingClockData.upstreamTimeProperties != data.TimePropertiesDS {
		e.LeadingClockData.upstreamTimeProperties = &data.TimePropertiesDS
		updateDownstream = true
	}
	if *e.LeadingClockData.upstreamParentDataSet != data.ParentDataSet {
		e.LeadingClockData.upstreamParentDataSet = &data.ParentDataSet
		updateDownstream = true
	}
	if e.LeadingClockData.upstreamCurrentDSStepsRemoved != data.CurrentDS.StepsRemoved {
		e.LeadingClockData.upstreamCurrentDSStepsRemoved = data.CurrentDS.StepsRemoved
		updateDownstream = true
	}
	if _, ok := e.clkSyncState[cfgName]; ok && updateDownstream {
		e.updateDownstreamData(cfgName, c)
	}
}

func (e *EventHandler) updateDownstreamData(cfgName string, c net.Conn) {
	if data, ok := e.clkSyncState[cfgName]; !ok {
		return
	} else if data.state == PTP_LOCKED {
		go e.downstreamAnnounceIWF(
			cfgName, c,
			*e.LeadingClockData.upstreamParentDataSet,
			*e.LeadingClockData.upstreamTimeProperties,
			e.LeadingClockData.upstreamCurrentDSStepsRemoved,
		)
	} else {
		go e.announceLocalData(cfgName, c)
	}
}

// EmitClockClass emits the current clock class and accuracy for the specified configuration.
func (e *EventHandler) EmitClockClass(cfgName string, c net.Conn) {
	if _, ok := e.clkSyncState[cfgName]; !ok {
		return
	}
	e.AnnounceClockClass(e.clkSyncState[cfgName].clockClass, e.clkSyncState[cfgName].clockAccuracy, cfgName, c)
}

// AnnounceClockClass announces clock class changes to the event handler and writes to the connection.
func (e *EventHandler) AnnounceClockClass(clockClass fbprotocol.ClockClass, clockAcc fbprotocol.ClockAccuracy, cfgName string, c net.Conn) {
	e.clockClass = clockClass
	e.clockAccuracy = clockAcc

	utils.EmitClockClass(c, PTP4lProcessName, cfgName, e.clockClass)
	if !e.stdoutToSocket && e.clockClassMetric != nil {
		e.clockClassMetric.With(prometheus.Labels{
			"process": PTP4lProcessName, "config": cfgName, "node": e.nodeName}).Set(float64(clockClass))
	}
}

// Implements Rec. ITU-T G.8275 (2024) Amd. 1 (08/2024)
// Table VIII.3 − T-BC-/ T-BC-P/ T-BC-A Announce message contents
// for free-run (acquiring), holdover within / out of the specification
func (e *EventHandler) announceLocalData(cfgName string, c net.Conn) {
	egp := protocol.ExternalGrandmasterProperties{
		GrandmasterIdentity: e.LeadingClockData.clockID,
		StepsRemoved:        0,
	}
	glog.Infof("EGP %++v", egp)
	go pmc.RunPMCExpSetExternalGMPropertiesNP(e.LeadingClockData.controlledPortsConfig, egp)
	e.AnnounceClockClass(e.clkSyncState[cfgName].clockClass, e.clkSyncState[cfgName].clockAccuracy, cfgName, c)
	gs := protocol.GrandmasterSettings{
		ClockQuality: fbprotocol.ClockQuality{
			ClockClass:              e.clkSyncState[cfgName].clockClass,
			ClockAccuracy:           fbprotocol.ClockAccuracyUnknown,
			OffsetScaledLogVariance: 0xffff,
		},
		TimePropertiesDS: protocol.TimePropertiesDS{
			TimeSource: fbprotocol.TimeSourceInternalOscillator,
		},
	}
	switch e.clkSyncState[cfgName].clockClass {
	case protocol.ClockClassFreerun:
		gs.TimePropertiesDS.CurrentUtcOffsetValid = false
		gs.TimePropertiesDS.Leap59 = false
		gs.TimePropertiesDS.Leap61 = false
		gs.TimePropertiesDS.PtpTimescale = true
		gs.TimePropertiesDS.TimeTraceable = false
		// TODO: get the real freq traceability status when implemented
		gs.TimePropertiesDS.FrequencyTraceable = false
		gs.TimePropertiesDS.CurrentUtcOffset = int32(leap.GetUtcOffset())
	case fbprotocol.ClockClass(165), fbprotocol.ClockClass(135):
		if e.LeadingClockData.downstreamTimeProperties == nil {
			glog.Info("Pending upstream clock data acquisition, skip updates")
			return
		}
		gs.TimePropertiesDS.CurrentUtcOffsetValid = e.LeadingClockData.downstreamTimeProperties.CurrentUtcOffsetValid
		gs.TimePropertiesDS.Leap59 = e.LeadingClockData.downstreamTimeProperties.Leap59
		gs.TimePropertiesDS.Leap61 = e.LeadingClockData.downstreamTimeProperties.Leap61
		gs.TimePropertiesDS.PtpTimescale = true
		if e.clkSyncState[cfgName].clockClass == fbprotocol.ClockClass(135) {
			gs.TimePropertiesDS.TimeTraceable = true
		} else {
			gs.TimePropertiesDS.TimeTraceable = false
		}
		// TODO: get the real freq traceability status when implemented
		gs.TimePropertiesDS.FrequencyTraceable = false
		gs.TimePropertiesDS.CurrentUtcOffset = e.LeadingClockData.downstreamTimeProperties.CurrentUtcOffset

	default:
	}
	go pmc.RunPMCExpSetGMSettings(e.LeadingClockData.controlledPortsConfig, gs)
}

// this function runs in a goroutine should only be called when locked
func (e *EventHandler) downstreamAnnounceIWF(cfgName string, c net.Conn, upstreamParentDataSet protocol.ParentDataSet, upstreamTimeProperties protocol.TimePropertiesDS, stepsRemoved uint16) {
	ptpCfgName := strings.Replace(cfgName, "ts2phc", "ptp4l", 1)
	glog.Infof("downstreamAnnounceIWF: %s", ptpCfgName)

	gs := protocol.GrandmasterSettings{
		ClockQuality: fbprotocol.ClockQuality{
			ClockClass:              fbprotocol.ClockClass(upstreamParentDataSet.GrandmasterClockClass),
			ClockAccuracy:           fbprotocol.ClockAccuracy(upstreamParentDataSet.GrandmasterClockAccuracy),
			OffsetScaledLogVariance: upstreamParentDataSet.GrandmasterOffsetScaledLogVariance,
		},
		TimePropertiesDS: upstreamTimeProperties,
	}
	es := protocol.ExternalGrandmasterProperties{
		GrandmasterIdentity: upstreamParentDataSet.GrandmasterIdentity,
		// stepsRemoved at this point is already incremented, representing the current clock position
		StepsRemoved: stepsRemoved,
	}
	glog.Infof("%++v", es)
	e.AnnounceClockClass(gs.ClockQuality.ClockClass, gs.ClockQuality.ClockAccuracy, cfgName, c)
	if err := pmc.RunPMCExpSetExternalGMPropertiesNP(e.LeadingClockData.controlledPortsConfig, es); err != nil {
		glog.Error(err)
	}
	if err := pmc.RunPMCExpSetGMSettings(e.LeadingClockData.controlledPortsConfig, gs); err != nil {
		glog.Error(err)
	}
	glog.Infof("%++v", es)

	// As we gave updated the downstream lets set the datasets
	e.LeadingClockData.downstreamParentDataSet = &upstreamParentDataSet
	e.LeadingClockData.downstreamTimeProperties = &upstreamTimeProperties
	e.LeadingClockData.upstreamCurrentDSStepsRemoved = stepsRemoved
}

func (e *EventHandler) inSyncCondition(cfgName string) bool {
	if e.LeadingClockData.inSyncConditionThreshold == 0 {
		glog.Info("Leading clock in-sync condition is pending initialization")
		return false
	}

	worstOffset := e.getLargestOffset(cfgName)
	if math.Abs(float64(worstOffset)) < float64(e.LeadingClockData.inSyncConditionThreshold) {
		e.LeadingClockData.inSyncThresholdCounter++
		if e.LeadingClockData.inSyncThresholdCounter >= e.LeadingClockData.inSyncConditionTimes {
			return true
		}
	} else {
		e.LeadingClockData.inSyncThresholdCounter = 0
	}

	glog.Info("sync condition not reached: worst offset ", worstOffset, " count ",
		e.LeadingClockData.inSyncThresholdCounter, " out of ", e.LeadingClockData.inSyncConditionTimes)

	return false
}

func (e *EventHandler) isSourceLostBC(cfgName string) bool {
	ptpLost := true
	dpllLost := false
	dpllLostIface := ""
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			if d.ProcessName == PTP4l {
				for _, dd := range d.Details {
					if dd.State == PTP_LOCKED {
						ptpLost = false
					}
				}
			}
			if d.ProcessName == DPLL {
				for _, dd := range d.Details {
					if dd.State != PTP_LOCKED {
						dpllLost = true
						dpllLostIface = dd.IFace
						break
					}
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

func (e *EventHandler) getLargestOffset(cfgName string) int64 {
	worstOffset := FaultyPhaseOffset
	staleTime := (time.Now().Unix() - StaleEventAfter) * 1000
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			for _, dd := range d.Details {
				// Skip stale data for all offsets, including the first one
				if dd.time < staleTime {
					continue
				}
				if worstOffset == FaultyPhaseOffset {
					if dd.IFace == e.clkSyncState[cfgName].leadingIFace {
						worstOffset = int64(d.window.Mean())
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
	}
	glog.Info("Largest offset ", worstOffset)
	return worstOffset
}

func (e *EventHandler) freeRunCondition(cfgName string) bool {
	if e.LeadingClockData.toFreeRunThreshold == 0 {
		glog.Info("Leading clock free-run condition is pending initialization")
		return true
	}
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			if d.ProcessName == DPLL {
				for _, dd := range d.Details {
					if dd.IFace == e.clkSyncState[cfgName].leadingIFace {
						if math.Abs(float64(dd.Offset)) > float64(e.LeadingClockData.toFreeRunThreshold) {
							glog.Infof("free-run condition on DPLL ", dd.IFace)
							return true
						}
					}
				}
			}
		}
	}
	return false
}

func (e *EventHandler) inSpecCondition(cfgName string) bool {
	if e.LeadingClockData.MaxInSpecOffset == 0 {
		glog.Info("Leading clock in-spec condition is pending initialization")
		return false
	}
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			if d.ProcessName == DPLL {
				for _, dd := range d.Details {
					if dd.IFace == e.clkSyncState[cfgName].leadingIFace {
						if math.Abs(float64(dd.Offset)) > float64(e.LeadingClockData.MaxInSpecOffset) {
							glog.Infof("out-of-spec condition on DPLL ", dd.IFace)
							return false
						}
					}
				}
			}
		}
	}
	return true
}

func (e *EventHandler) getLeadingInterfaceBC() string {
	if e.LeadingClockData.leadingInterface != "" {
		return e.LeadingClockData.leadingInterface
	}
	return LEADING_INTERFACE_UNKNOWN
}

func (e *EventHandler) convergeConfig(event EventChannel) EventChannel {
	if event.ProcessName == PTP4lProcessName {
		iface := event.IFace
		for cfg, dd := range e.data {
			for _, item := range dd {
				if item.ProcessName != DPLL {
					continue
				}
				for _, dp := range item.Details {
					if utils.GetAlias(dp.IFace) == utils.GetAlias(iface) {
						// We want to process ptp4l having a separate config with ts2phc and dpll events having ts2phc config
						// so in the rare occurrence of ptp4l state change we modify the event.CfgName
						event.CfgName = cfg
					}
				}
			}
		}
	}
	e.updateLeadingClockData(event)
	return event
}

func (e *EventHandler) updateLeadingClockData(event EventChannel) {
	switch event.ProcessName {
	case PTP4lProcessName:
		cpc, found := event.Values[ControlledPortsConfig].(string)
		if found {
			e.LeadingClockData.controlledPortsConfig = cpc
		}
		id, found := event.Values[ClockIDKey].(string)
		if found {
			e.LeadingClockData.clockID = id
		}
	case DPLL:
		ls, found := event.Values[LeadingSource].(bool)
		if found && ls {
			e.LeadingClockData.leadingInterface = event.IFace
		}
		inSyncTh, found := event.Values[InSyncConditionThreshold].(uint64)
		if found {
			e.LeadingClockData.inSyncConditionThreshold = int(inSyncTh)
		}
		inSyncTimes, found := event.Values[InSyncConditionTimes].(uint64)
		if found {
			e.LeadingClockData.inSyncConditionTimes = int(inSyncTimes)
		}
		toFreeRunTh, found := event.Values[ToFreeRunThreshold].(uint64)
		if found {
			e.LeadingClockData.toFreeRunThreshold = int(toFreeRunTh)
		}
		maxInSpec, found := event.Values[MaxInSpecOffset].(uint64)
		if found {
			e.LeadingClockData.MaxInSpecOffset = maxInSpec
		}
	}
}
