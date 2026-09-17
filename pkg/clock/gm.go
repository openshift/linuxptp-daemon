package clock

import (
	"fmt"
	"strings"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/debug"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

// GM is a Grand Master clock instance.
type GM struct {
	cfgName                string
	sendIPC                func(ipc.Message)
	getUtcOffset           func() int
	pmcClient              pmc.Client
	data                   []*event.Data
	syncState              SyncState
	overallSyncState       event.PTPState
	osClockState           event.PTPState
	gnssState              event.PTPState
	announcedClockClass    fbprotocol.ClockClass
	announcedClockAccuracy fbprotocol.ClockAccuracy
	lastLoggedState        event.PTPState
}

// ClockType returns the clock type for this GM clock.
func (c *GM) ClockType() event.ClockType { return event.GM }

// ClockClass returns the current clock class.
func (c *GM) ClockClass() fbprotocol.ClockClass { return c.syncState.ClockClass }

// ConfigName returns the configuration name.
func (c *GM) ConfigName() string { return c.cfgName }

// GetState returns the current PTP synchronization state.
func (c *GM) GetState() event.PTPState { return c.syncState.State }

// Reset resets the clock state.
func (c *GM) Reset() {
	c.syncState = SyncState{}
	c.overallSyncState = event.PTP_FREERUN
	c.osClockState = event.PTP_NOTSET
	c.gnssState = event.PTP_NOTSET
	c.announcedClockClass = 0
	c.announcedClockAccuracy = 0
	c.lastLoggedState = ""
	c.data = nil
}

// AddEvent processes an event and updates clock state.
func (c *GM) AddEvent(ev event.Event) SyncState {
	if ev.Source == event.PMC {
		return c.syncState
	}

	d := c.getData(ev.Source)
	d.AddEvent(ev)
	d.UpdateState()
	clockState := c.updateState()
	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState.State, c.osClockState, c.cfgName)
	c.announceClockClassIfChanged(ev, clockState)

	// Zero NMEA status when GM is not locked
	if clockState.State != event.PTP_LOCKED {
		if ptp, isPTP := ev.Data.(*event.PTPData); isPTP {
			if _, hasNMEA := ptp.Values[event.NMEA_STATUS]; hasNMEA {
				ptp.Values[event.NMEA_STATUS] = 0
			}
		}
	}

	// Debug tree updates
	dataDetails := d.GetDataDetails(ev.IFace)
	if dataDetails != nil {
		switch data := ev.Data.(type) {
		case *event.GNSSData:
			debug.UpdateGNSSState(string(dataDetails.State), data.Offset)
		case *event.PTPData:
			switch ev.Source {
			case event.DPLL:
				debug.UpdateDPLLState(string(data.State), data.Values[event.OFFSET], ev.IFace)
				debug.UpdateDPLLState(string(dataDetails.State), 0, debug.OverallDpllKey)
			case event.TS2PHC:
				debug.UpdateTs2phcState(string(data.State), data.Values[event.OFFSET], ev.IFace)
				debug.UpdateTs2phcState(string(dataDetails.State), 0, debug.OverallTs2phcKey)
			}
		}
	}
	debug.UpdateGMState(string(clockState.State))

	return clockState
}

func (c *GM) getData(processName event.EventSource) *event.Data {
	for _, d := range c.data {
		if d.ProcessName == processName {
			return d
		}
	}
	d := &event.Data{ProcessName: processName, State: event.PTP_UNKNOWN, Window: *utils.NewWindow(event.WindowSize)}
	c.data = append(c.data, d)
	return d
}

func (c *GM) announceClockClassIfChanged(ev event.Event, clockState SyncState) {
	if clockState.ClockClass == protocol.ClockClassUninitialized {
		return
	}

	clockAccuracy := c.announcedClockAccuracy
	if ptp, isPTP := ev.Data.(*event.PTPData); isPTP && ev.Source == event.DPLL {
		if clockState.ClockClass == fbprotocol.ClockClass7 || clockState.ClockClass == protocol.ClockClassOutOfSpec {
			if offset, found := ptp.Values[event.OFFSET]; found {
				if offsetValue, isInt64 := offset.(int64); isInt64 {
					clockAccuracy = fbprotocol.ClockAccuracyFromOffset(time.Duration(offsetValue) * time.Nanosecond)
				}
			}
		}
	}

	if clockState.ClockClass != c.announcedClockClass || clockAccuracy != c.announcedClockAccuracy {
		glog.Infof("clock class change request from %d to %d with clock accuracy from %d to %d",
			uint8(c.announcedClockClass), uint8(clockState.ClockClass),
			uint8(c.announcedClockAccuracy), uint8(clockAccuracy))
		c.syncState.ClockAccuracy = clockAccuracy
		clockClass, _, err := c.announceClockClass()
		debug.UpdateClockClass(uint8(clockState.ClockClass))
		if err != nil {
			glog.Errorf("error updating clock class %s", err)
		} else {
			glog.Infof("updated clock class to %d", clockClass)
			clockClassOut := utils.GetClockClassLogMessage(event.PTP4lProcessName, c.cfgName, clockClass)
			fmt.Printf("%s", clockClassOut)
		}
	}

	if c.lastLoggedState != clockState.State {
		glog.Infof("PTP State: %v, Clock Class %d Time %s sourceLost %v",
			clockState.State, clockState.ClockClass, time.Now(), clockState.SourceLost)
		c.lastLoggedState = clockState.State
	}
}

func (c *GM) isSourceLost() bool {
	for _, d := range c.data {
		if d.ProcessName == event.GNSS && len(d.Details) > 0 && d.Details[0] != nil {
			return d.Details[0].SourceLost
		}
	}
	return false
}

func (c *GM) getLeadingInterface() string {
	for _, d := range c.data {
		if d.ProcessName == event.GNSS && len(d.Details) > 0 {
			return d.Details[0].IFace
		} else if d.ProcessName == event.TS2PHCProcessName && len(d.Details) > 0 {
			for _, dd := range d.Details {
				if dd.SignalSource == event.GNSS {
					return dd.IFace
				}
			}
		}
	}
	return event.LEADING_INTERFACE_UNKNOWN
}

func (c *GM) hasNonLeadingDPLLFault(leadingInterface string) bool {
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

// SystemClockUpdate updates the OS clock state.
func (c *GM) SystemClockUpdate(osClockState event.PTPState) {
	c.osClockState = osClockState
	emitOverallSyncStateIfChanged(c.sendIPC, &c.overallSyncState, c.syncState.State, c.osClockState, c.cfgName)
}

func (c *GM) updateClockClass(clockClass fbprotocol.ClockClass) {
	if clockClass == c.syncState.ClockClass {
		return
	}
	c.syncState.ClockClass = clockClass
	c.sendIPC(ipc.Message{
		Type:    ipc.TypeClockClass,
		Profile: c.cfgName,
		IFace:   c.syncState.LeadingIFace,
		Values:  ipc.ClockClassValue{ClockClass: uint8(clockClass)},
	})
}

func (c *GM) announceClockClass() (clockClass fbprotocol.ClockClass, clockAccuracy fbprotocol.ClockAccuracy, err error) {
	pmcCfgName := strings.Replace(c.cfgName, event.TS2PHCProcessName, event.PTP4lProcessName, 1)
	g, err := c.pmcClient.GetGMSettings(pmcCfgName)
	if err != nil {
		glog.Errorf("failed to get current GRANDMASTER_SETTINGS_NP: %s", err)
		return clockClass, clockAccuracy, err
	}
	g.TimePropertiesDS.PtpTimescale = true
	g.TimePropertiesDS.FrequencyTraceable = true
	g.TimePropertiesDS.CurrentUtcOffsetValid = true
	g.TimePropertiesDS.CurrentUtcOffset = int32(c.getUtcOffset())
	switch c.syncState.ClockClass {
	case fbprotocol.ClockClass6:
		if g.ClockQuality.ClockClass != fbprotocol.ClockClass6 || !g.TimePropertiesDS.TimeTraceable {
			g.ClockQuality.ClockClass = fbprotocol.ClockClass6
			g.TimePropertiesDS.TimeTraceable = true
			g.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceGNSS
			g.ClockQuality.OffsetScaledLogVariance = 0x4e5d
			err = c.pmcClient.SetGMSettings(pmcCfgName, g)
		}
	case protocol.ClockClassOutOfSpec:
		if g.ClockQuality.ClockClass != protocol.ClockClassOutOfSpec {
			g.ClockQuality.ClockClass = protocol.ClockClassOutOfSpec
			g.TimePropertiesDS.TimeTraceable = false
			g.ClockQuality.ClockAccuracy = c.syncState.ClockAccuracy
			g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceInternalOscillator
			g.ClockQuality.OffsetScaledLogVariance = 0xffff
			err = c.pmcClient.SetGMSettings(pmcCfgName, g)
		}
	case fbprotocol.ClockClass7:
		if g.ClockQuality.ClockClass != fbprotocol.ClockClass7 {
			g.ClockQuality.ClockClass = fbprotocol.ClockClass7
			g.TimePropertiesDS.TimeTraceable = true
			g.ClockQuality.ClockAccuracy = c.syncState.ClockAccuracy
			g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceInternalOscillator
			g.ClockQuality.OffsetScaledLogVariance = 0xffff
			err = c.pmcClient.SetGMSettings(pmcCfgName, g)
		}
	case protocol.ClockClassFreerun:
		if g.ClockQuality.ClockClass != protocol.ClockClassFreerun {
			g.ClockQuality.ClockClass = protocol.ClockClassFreerun
			g.TimePropertiesDS.TimeTraceable = false
			g.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
			g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceInternalOscillator
			g.ClockQuality.OffsetScaledLogVariance = 0xffff
			err = c.pmcClient.SetGMSettings(pmcCfgName, g)
		}
	default:
		glog.Infof("No clock class identified for %d", c.syncState.ClockClass)
		err = fmt.Errorf("no clock class identified for %d", c.syncState.ClockClass)
	}
	if err == nil {
		c.announcedClockClass = g.ClockQuality.ClockClass
		c.announcedClockAccuracy = g.ClockQuality.ClockAccuracy
	}
	return g.ClockQuality.ClockClass, g.ClockQuality.ClockAccuracy, err
}

func (c *GM) status() string {
	return fmt.Sprintf("%s[%d]:[%s] %s T-GM-STATUS %s\n", event.GM, c.syncState.LastLoggedTime, c.cfgName,
		c.syncState.LeadingIFace, c.syncState.State)
}

// getGMState ... get lowest state of all the interfaces
/*
GNSS State + DPLL State= DPLL State
DPLL STate + Ts2phc State =GM State
----------------------------------------------------------------
GNSS| Mode              | Offset   | State
1.  | 0-2(Source LOST)  | in Range | FREERUN
2.  | 0-2(Source LOST ) | out Range| FREERUN
3.  | 3                 | in Range | LOCKED
4.  | 3                 | out Range| FREERUN
----------------------------------------------------------------
DPLL | Frequency/Phase  	|  Offset  | GNSS STATE |  DPLL PTP STATE
------------------------------------------------------------------
1.  | -1/1/0           	| in Range |  LOCKED    | FREERUN
2.  | -1/1/0           	| out Range|  FREERUN   | FREERUN
-----------------------------------------------------------------
3.  |  2 (LOCKED)       	| in Range |  LOCKED      | LOCKED
4.  |  2 (LOCKED)       	| in Range |  FREERUN     | LOCKED
-----------------------------------------------------------------
SL :-> Source Lost
------------------------------------------------------------------------------------------
DPLL| Frequency/Phase      | Offset      | GNSS STATE               | DPLL PTP State
------------------------------------------------------------------------------------------
5   | 2 (LOCKED)           | Out Range   | All State                | FREERUN
6.  | 3 (LOCK_ACQ_HOLDOVER)| In Range    | LOCKED                   | LOCKED
7.  | 3 (LOCK_ACQ_HOLDOVER)| In/Out Range| FREERUN (SL)             | FREERUN
8.  | 3 (LOCK_ACQ_HOLDOVER)| Out Range   | LOCKED                   | FREERUN
*9. | 3 (LOCK_ACQ_HOLDOVER)| In/Out Range| FREERUN (SL)             | HOLDOVER
------------------------------------------------------------------------------------------
*10.| 4 (HOLDOVER)		| IN/Out Range   | FREERUN (SL)	            | HOLDOVER
*11.| 4 (HOLDOVER)		| in/Out Range   | FREERUN (SL)             | AFTER TIME OUT
                                                                    FREERUN OUT OF SPEC

12. | 4 (HOLDOVER)		| in Range	     | LOCKED                   | LOCKED
13. | 4 (HOLDOVER)		| Out Range	     | LOCKED                   | FREERUN
14. | 4 (HOLDOVER)		| in Range       | FREERUN (SL)             | LOCKED
15. | 4 (HOLDOVER)		| Out Range      | FREERUN (SL)             | FREERUN
------------------------------------------------------------------------------------------
FINAL GM STATE  *SL = Source Lost
---------------------------------------------------------------------------------------------
| DPLL PTP State        | GNSS PTP STATE    | TS2PHC PTP STATE | GM STATE  | Clock Class
---------------------------------------------------------------------------------------------
| FREERUN               | NA                | NA                | FREERUN  | 248
| HOLDOVER IN SPEC      | NA                | NA                | HOLDOVER | 7
| FREERUN OUT OF SPEC   | NA                | NA                | FREERUN  | 140
| LOCKED                | LOCKED            | LOCKED            | LOCKED   | 6
| LOCKED                | LOCKED            | FREERUN           | FREERUN  | 248
| LOCKED                | *FREERUN (SL)     | LOCKED            | NA       | Wait for DPLL
                                                                           | to move to HOLDOVER

| LOCKED                | *FREERUN (SL)     | FREERUN           | NA       | Wait for DPLL
                                                                           |to move to HOLDOVER

| LOCKED                | *FREERUN(offset)  | LOCKED            | FREERUN  | 248
| LOCKED                | *FREERUN(offset)  | FREERUN           | FREERUN  | 248
 Final GM State When DPLL not available
---------------------------------------------------------------------------------------------
DPLL PTP State |  GNSS PTP STATE  |	TS2PHC PTP STATE| GM STATE | Clock Class
---------------------------------------------------------------------------------------------
| NA           |  FREERUN         |	LOCKED          | FREERUN  | 248
| NA           |  FREERUN         |	FREERUN         | FREERUN  | 248
| NA           |  LOCKED          |	FREERUN         | FREERUN  | 248
| NA           |  LOCKED          |	LOCKED          | LOCKED   | 6

*/
// updateState computes the composite GM state from DPLL, GNSS, and ts2phc
// data sources. It emits TypePTPState and TypeClockClass IPC on state/class change.
func (c *GM) updateState() SyncState {
	dpllState := event.PTP_NOTSET
	gnssState := event.PTP_FREERUN
	ts2phcState := event.PTP_FREERUN
	syncSrcLost := c.isSourceLost()
	leadingInterface := c.getLeadingInterface()
	glog.Infof("GM updateState: leadingInterface=%s, data entries=%d", leadingInterface, len(c.data))
	for _, d := range c.data {
		glog.Infof("  Data: process=%s state=%s details=%d", d.ProcessName, d.State, len(d.Details))
	}
	if leadingInterface == event.LEADING_INTERFACE_UNKNOWN {
		glog.Infof("Leading interface is not yet identified, clock state reporting delayed.")
		return SyncState{LeadingIFace: leadingInterface}
	}

	c.syncState.SourceLost = syncSrcLost
	c.syncState.LeadingIFace = leadingInterface

	var outOfSpec, frequencyTraceable bool
	if c.data != nil {
		for _, d := range c.data {
			switch d.ProcessName {
			case event.DPLL:
				dpllState = d.State
				if c.hasNonLeadingDPLLFault(leadingInterface) {
					dpllState = event.PTP_FREERUN
				}
				if dd := d.GetDataDetails(leadingInterface); dd != nil {
					outOfSpec = dd.OutOfSpec
					frequencyTraceable = dd.FrequencyTraceable
				}
			case event.GNSS:
				gnssState = d.State
			case event.TS2PHCProcessName:
				ts2phcState = d.State
				if parser.NoSourceTSCount == 2 {
					ts2phcState = event.PTP_FREERUN
				}
			}
		}
	} else {
		c.syncState.State = event.PTP_FREERUN
		c.syncState.ClockClass = protocol.ClockClassFreerun
		c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
		c.syncState.LastLoggedTime = time.Now().Unix()
		glog.Info(c.status())
		return c.syncState
	}

	prevState := c.syncState.State
	prevClass := c.syncState.ClockClass

	switch dpllState {
	case event.PTP_FREERUN:
		c.syncState.State = dpllState
		if outOfSpec && frequencyTraceable {
			c.syncState.ClockClass = protocol.ClockClassOutOfSpec
		} else {
			c.syncState.ClockClass = protocol.ClockClassFreerun
		}
		c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
	case event.PTP_HOLDOVER:
		c.syncState.State = dpllState
		c.syncState.ClockClass = fbprotocol.ClockClass7
	case event.PTP_LOCKED, event.PTP_NOTSET:
		switch gnssState {
		case event.PTP_LOCKED:
			switch ts2phcState {
			case event.PTP_FREERUN:
				c.syncState.State = event.PTP_FREERUN
				c.syncState.ClockClass = protocol.ClockClassFreerun
				c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
			case event.PTP_LOCKED:
				c.syncState.State = event.PTP_LOCKED
				c.syncState.ClockClass = fbprotocol.ClockClass6
				c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			case event.PTP_HOLDOVER:
				c.syncState.State = event.PTP_HOLDOVER
				c.syncState.ClockClass = fbprotocol.ClockClass7
			}
		case event.PTP_FREERUN:
			if syncSrcLost {
				switch ts2phcState {
				case event.PTP_LOCKED:
				case event.PTP_FREERUN:
					c.syncState.State = event.PTP_FREERUN
				case event.PTP_HOLDOVER:
					c.syncState.State = event.PTP_HOLDOVER
					c.syncState.ClockClass = fbprotocol.ClockClass7
				}
			} else {
				switch ts2phcState {
				case event.PTP_FREERUN, event.PTP_LOCKED, event.PTP_UNKNOWN, event.PTP_NOTSET:
					c.syncState.State = event.PTP_FREERUN
					c.syncState.ClockClass = protocol.ClockClassFreerun
					c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
				}
			}
		}
	default:
		switch gnssState {
		case event.PTP_LOCKED:
			switch ts2phcState {
			case event.PTP_FREERUN, event.PTP_UNKNOWN, event.PTP_NOTSET:
				c.syncState.State = event.PTP_FREERUN
				c.syncState.ClockClass = protocol.ClockClassFreerun
				c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
			case event.PTP_LOCKED:
				c.syncState.State = event.PTP_LOCKED
				c.syncState.ClockClass = fbprotocol.ClockClass6
				c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			case event.PTP_HOLDOVER:
				c.syncState.State = event.PTP_HOLDOVER
				c.syncState.ClockClass = fbprotocol.ClockClass7
			}
		case event.PTP_FREERUN:
			switch ts2phcState {
			case event.PTP_FREERUN, event.PTP_LOCKED, event.PTP_UNKNOWN, event.PTP_NOTSET:
				c.syncState.State = event.PTP_FREERUN
				c.syncState.ClockClass = protocol.ClockClassFreerun
				c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
			case event.PTP_HOLDOVER:
				c.syncState.State = event.PTP_HOLDOVER
				c.syncState.ClockClass = fbprotocol.ClockClass7
			}
		default:
			c.syncState.State = ts2phcState
			switch ts2phcState {
			case event.PTP_FREERUN:
				c.syncState.ClockClass = protocol.ClockClassFreerun
				c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
			case event.PTP_LOCKED:
				c.syncState.ClockClass = fbprotocol.ClockClass7
				c.syncState.ClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			}
		}
	}

	if gnssState != c.gnssState {
		c.gnssState = gnssState
		c.sendIPC(ipc.Message{
			Type:    ipc.TypeGNSSState,
			Profile: c.cfgName,
			IFace:   leadingInterface,
			Values:  ipc.GNSSStateValue{State: event.PtpStateToIPCState(gnssState)},
		})
	}

	if c.syncState.State != prevState && prevState != event.PTP_NOTSET {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypePTPState,
			Profile: c.cfgName,
			IFace:   leadingInterface,
			Values:  ipc.StateValue{State: event.PtpStateToIPCState(c.syncState.State)},
		})
	}

	if c.syncState.ClockClass != prevClass && prevClass != protocol.ClockClassUninitialized {
		c.sendIPC(ipc.Message{
			Type:    ipc.TypeClockClass,
			Profile: c.cfgName,
			IFace:   leadingInterface,
			Values:  ipc.ClockClassValue{ClockClass: uint8(c.syncState.ClockClass)},
		})
	}

	result := SyncState{
		State:         c.syncState.State,
		ClockClass:    c.syncState.ClockClass,
		ClockAccuracy: c.syncState.ClockAccuracy,
		SourceLost:    c.syncState.SourceLost,
		LeadingIFace:  c.syncState.LeadingIFace,
	}

	logTime := time.Now().Unix()
	if c.syncState.LastLoggedTime != logTime {
		c.syncState.LastLoggedTime = logTime
		glog.Info(c.status())
		glog.Infof("dpll State %s, gnss State %s, tsphc state %s, gm state %s,", dpllState, gnssState, ts2phcState, c.syncState.State)
	}
	return result
}
