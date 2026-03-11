package event

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"

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

// updateBCState updates the BC/TSC state machine.
// Called with e.Lock() held. Returns the clock sync state and whether a TTSC clock class
// announcement is needed (the caller must perform the I/O after releasing the lock).
func (e *EventHandler) updateBCState(event EventChannel) (clockSyncState, bool) {
	cfgName := event.CfgName
	dpllState := PTP_NOTSET
	ts2phcState := PTP_FREERUN
	// For internal data announces, only update the downstream data on class change
	// For External GM data announces in the locked state, update whenever any of the
	// information elements change
	updateDownstreamData := false
	if event.ProcessName == PTP4lProcessName {
		glog.Infof("PTP4l event: %+v", event)
	}
	leadingInterface := e.getLeadingInterfaceBC()
	if leadingInterface == LEADING_INTERFACE_UNKNOWN {
		glog.Infof("Leading interface is not yet identified, clock state reporting delayed.")
		return clockSyncState{leadingIFace: leadingInterface}, false
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
				ts2phcState = d.State
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
		return *e.clkSyncState[cfgName], false
	}

	isTTSC := (e.LeadingClockData.clockID != "" && e.LeadingClockData.controlledPortsConfig == "")

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
			if e.LeadingClockData.upstreamParentDataSet.GrandmasterClockClass == uint8(protocol.ClockClassFreerun) {
				updateDownstreamData = false // Don't propagate uptream free run and instead let future call move to holdover/freerun
			} else if e.clkSyncState[cfgName].clockClass != fbprotocol.ClockClass(e.LeadingClockData.upstreamParentDataSet.GrandmasterClockClass) && !isTTSC {
				e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass(e.LeadingClockData.upstreamParentDataSet.GrandmasterClockClass)
				e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracy(e.LeadingClockData.upstreamParentDataSet.GrandmasterClockAccuracy)
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

	switch gSycState.state {
	case PTP_FREERUN:
		e.clkSyncState[cfgName].clockOffset = FaultyPhaseOffset
	case PTP_HOLDOVER:
		e.clkSyncState[cfgName].clockOffset = e.getCalculatedHoldoverOffset(cfgName)
	default:
		e.clkSyncState[cfgName].clockOffset = e.getLargestOffset(cfgName)
	}

	if isTTSC && e.clkSyncState[cfgName].clockClass != fbprotocol.ClockClassSlaveOnly {
		e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClassSlaveOnly
	}
	needsTTSCAnnounce := false
	if updateDownstreamData && e.clkSyncState[cfgName].clockClass != protocol.ClockClassUninitialized {
		if isTTSC {
			// Set clock class fields under lock; the caller will emit after releasing the lock
			e.setClockClassLocked(e.clkSyncState[cfgName].clockClass, e.clkSyncState[cfgName].clockAccuracy)
			needsTTSCAnnounce = true
		} else {
			go e.updateDownstreamData(cfgName)
		}
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
	return rclockSyncState, needsTTSCAnnounce
}

// UpdateUpstreamParentDataSet updates the upstream time properties, parent data set, and current data set
// for the leading clock and triggers downstream data updates when changes are detected.
func (e *EventHandler) UpdateUpstreamParentDataSet(parentDS protocol.ParentDataSet) {
	if !parentDS.Equal(e.LeadingClockData.upstreamParentDataSet) {
		e.LeadingClockData.upstreamParentDataSet = &parentDS
	}
}

func (e *EventHandler) updateDownstreamData(cfgName string) {
	e.Lock()
	data, ok := e.clkSyncState[cfgName]
	if !ok {
		e.Unlock()
		return
	}
	state := data.state

	// Cancel any in-flight downstream update for this config before launching
	// a new one. This prevents stale goroutines from overwriting state that a
	// newer transition has already set.
	if cancel, exists := e.downstreamCancel[cfgName]; exists {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.downstreamCancel[cfgName] = cancel
	e.Unlock()

	if state == PTP_LOCKED {
		go e.downstreamAnnounceIWF(ctx, cfgName)
	} else {
		go e.announceLocalData(cfgName)
	}
}

// EmitClockClass emits the current clock class and accuracy for the specified configuration.
// Broken pipe errors are handled internally via signalBrokenPipe.
func (e *EventHandler) EmitClockClass(cfgName string) {
	e.Lock()
	state, ok := e.clkSyncState[cfgName]
	if !ok {
		e.Unlock()
		return
	}
	clockClass := state.clockClass
	clockAccuracy := state.clockAccuracy
	e.Unlock()
	e.announceClockClass(clockClass, clockAccuracy, cfgName)
}

// Implements Rec. ITU-T G.8275 (2024) Amd. 1 (08/2024)
// Table VIII.3 − T-BC-/ T-BC-P/ T-BC-A Announce message contents
// for free-run (acquiring), holdover within / out of the specification
func (e *EventHandler) announceLocalData(cfgName string) {
	// Snapshot shared data under lock to prevent data races with updateBCState
	e.Lock()
	clockID := e.LeadingClockData.clockID
	controlledPortsConfig := e.LeadingClockData.controlledPortsConfig
	downstreamTimeProperties := e.LeadingClockData.downstreamTimeProperties
	state, ok := e.clkSyncState[cfgName]
	if !ok {
		e.Unlock()
		return
	}
	clockClass := state.clockClass
	clockAccuracy := state.clockAccuracy
	e.Unlock()

	egp := protocol.ExternalGrandmasterProperties{
		GrandmasterIdentity: clockID,
		StepsRemoved:        0,
	}
	glog.Infof("EGP %++v", egp)
	go func() {
		if err := pmc.RunPMCExpSetExternalGMPropertiesNP(controlledPortsConfig, egp); err != nil {
			glog.Errorf("Failed to set external GM properties: %v", err)
		}
	}()
	e.announceClockClass(clockClass, clockAccuracy, cfgName)
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
		// TODO: get the real freq traceability status when implemented
		gs.TimePropertiesDS.FrequencyTraceable = false
		gs.TimePropertiesDS.CurrentUtcOffset = int32(leap.GetUtcOffset())
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
		// TODO: get the real freq traceability status when implemented
		gs.TimePropertiesDS.FrequencyTraceable = false
		gs.TimePropertiesDS.CurrentUtcOffset = downstreamTimeProperties.CurrentUtcOffset

	default:
	}
	go func() {
		if err := pmc.RunPMCExpSetGMSettings(controlledPortsConfig, gs); err != nil {
			glog.Errorf("Failed to set GM settings: %v", err)
		}
	}()
	go func() {
		if err := pmc.RunPMCExpSetGMSettings(cfgName, gs); err != nil {
			glog.Errorf("Failed to set GM settings: %v", err)
		}
	}()
}

// applyIfLockedBC acquires the lock, checks that the BC state is still
// PTP_LOCKED, and if so runs fn under the lock. Returns false if the state
// is no longer LOCKED (fn is not called). The lock is always released via defer.
func (e *EventHandler) applyIfLockedBC(cfgName, context string, fn func()) bool {
	e.Lock()
	defer e.Unlock()
	stateData, ok := e.clkSyncState[cfgName]
	if !ok || stateData.state != PTP_LOCKED {
		state := PTP_NOTSET
		if ok {
			state = stateData.state
		}
		glog.Infof("downstreamAnnounceIWF: BC state is %s (not LOCKED) %s, aborting", state, context)
		return false
	}
	fn()
	return true
}

// this function runs in a goroutine
func (e *EventHandler) downstreamAnnounceIWF(ctx context.Context, cfgName string) {
	ptpCfgName := strings.Replace(cfgName, "ts2phc", "ptp4l", 1)
	glog.Infof("downstreamAnnounceIWF: %s", ptpCfgName)

	// Snapshot controlledPortsConfig under lock
	e.Lock()
	controlledPortsConfig := e.LeadingClockData.controlledPortsConfig
	e.Unlock()

	upsteamData, fetchErr := pmc.RunPMCExpGetParentTimeAndCurrentDataSets(cfgName)
	if fetchErr != nil {
		glog.Error("Failed to fetch upstream data, downstream data can not be updated.")
		return
	}

	if ctx.Err() != nil {
		glog.Info("downstreamAnnounceIWF: cancelled after PMC fetch")
		return
	}

	if !e.applyIfLockedBC(cfgName, "after PMC fetch", func() {
		e.LeadingClockData.upstreamParentDataSet = &upsteamData.ParentDataSet
		e.LeadingClockData.upstreamTimeProperties = &upsteamData.TimePropertiesDS
		e.LeadingClockData.upstreamCurrentDSStepsRemoved = upsteamData.CurrentDS.StepsRemoved
	}) {
		return
	}

	if ctx.Err() != nil {
		glog.Info("downstreamAnnounceIWF: cancelled before announce")
		return
	}

	gs := protocol.GrandmasterSettings{
		ClockQuality: fbprotocol.ClockQuality{
			ClockClass:              fbprotocol.ClockClass(upsteamData.ParentDataSet.GrandmasterClockClass),
			ClockAccuracy:           fbprotocol.ClockAccuracy(upsteamData.ParentDataSet.GrandmasterClockAccuracy),
			OffsetScaledLogVariance: upsteamData.ParentDataSet.GrandmasterOffsetScaledLogVariance,
		},
		TimePropertiesDS: upsteamData.TimePropertiesDS,
	}
	es := protocol.ExternalGrandmasterProperties{
		GrandmasterIdentity: upsteamData.ParentDataSet.GrandmasterIdentity,
		// stepsRemoved at this point is already incremented, representing the current clock position
		StepsRemoved: upsteamData.CurrentDS.StepsRemoved,
	}
	glog.Infof("%++v", es)
	e.announceClockClass(gs.ClockQuality.ClockClass, gs.ClockQuality.ClockAccuracy, cfgName)
	if err := pmc.RunPMCExpSetExternalGMPropertiesNP(controlledPortsConfig, es); err != nil {
		glog.Error(err)
	}
	if err := pmc.RunPMCExpSetGMSettings(controlledPortsConfig, gs); err != nil {
		glog.Error(err)
	}
	glog.Infof("%++v", es)

	if ctx.Err() != nil {
		glog.Info("downstreamAnnounceIWF: cancelled before downstream update")
		return
	}

	e.applyIfLockedBC(cfgName, "after downstream announce", func() {
		e.LeadingClockData.downstreamParentDataSet = &upsteamData.ParentDataSet
		e.LeadingClockData.downstreamTimeProperties = &upsteamData.TimePropertiesDS
	})
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
	staleTime := time.Now().UnixMilli() - StaleEventAfter
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			if d.window.IsEmpty() {
				continue
			}
			if !d.window.IsFull() {
				glog.Infof("Largest offset %d (window not full for %s)", FaultyPhaseOffset, d.ProcessName)
				return FaultyPhaseOffset
			}
			for _, dd := range d.Details {
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

func (e *EventHandler) getCalculatedHoldoverOffset(cfgName string) int64 {
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			if d.ProcessName == DPLL {
				return int64(d.window.LastInserted())
			}
		}
	}
	return FaultyPhaseOffset // No DPLL entries yet return faulty
}

func (e *EventHandler) freeRunCondition(cfgName string) bool {
	if e.LeadingClockData.toFreeRunThreshold == 0 {
		glog.Info("Leading clock free-run condition is pending initialization")
		return true
	}
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			switch d.ProcessName {
			case DPLL:
				for _, dd := range d.Details {
					if dd.IFace == e.clkSyncState[cfgName].leadingIFace {
						if math.Abs(float64(dd.Offset)) > float64(e.LeadingClockData.toFreeRunThreshold) {
							glog.Infof("free-run condition on DPLL %s", dd.IFace)
							return true
						}
					}
				}
			case PTP4l:
				if d.window.IsEmpty() {
					continue
				}
				// Use the window mean rather than per-detail offsets: the active TR port
				// (which feeds the window via sendPtp4lOffsetEvent) may have a different
				// interface name than the DPLL leading interface on the same NIC.
				ptp4lAvgOffset := int64(d.window.Mean())
				if math.Abs(float64(ptp4lAvgOffset)) > float64(e.LeadingClockData.toFreeRunThreshold) {
					glog.Infof("free-run condition on PTP4l, avg offset %d", ptp4lAvgOffset)
					return true
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
					if alias.GetAlias(dp.IFace) == alias.GetAlias(iface) {
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
