package event

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/debug"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/prometheus/client_golang/prometheus"
)

type ValueType string

const (
	PTPNamespace = "openshift"
	PTPSubsystem = "ptp"
	WindowSize   = 10
)

// nolint:all
// TODO: fix ALL_CAPS and add comments to the exported keys
const (
	OFFSET     ValueType = "offset"
	STATE      ValueType = "state"
	GPS_STATUS ValueType = "gnss_status"
	//Status           ValueType = "status"
	PHASE_STATUS              ValueType = "phase_status"
	FREQUENCY_STATUS          ValueType = "frequency_status"
	NMEA_STATUS               ValueType = parserconstants.NmeaStatus
	PROCESS_STATUS            ValueType = "process_status"
	PPS_STATUS                ValueType = "pps_status"
	LEADING_INTERFACE_UNKNOWN string    = "unknown"
	DEVICE                    ValueType = "device"
	QL                        ValueType = "ql"
	EXT_QL                    ValueType = "ext_ql"
	CLOCK_QUALITY             ValueType = "clock_quality"
	NETWORK_OPTION            ValueType = "network_option"
	EEC_STATE                           = "eec_state"
)

var valueTypeHelpTxt = map[ValueType]string{
	OFFSET:           "0 = FREERUN, 1 = LOCKED, 2 = HOLDOVER",
	GPS_STATUS:       "0=NOFIX, 1=Dead Reckoning Only, 2=2D-FIX, 3=3D-FIX, 4=GPS+dead reckoning fix, 5=Time only fix",
	PHASE_STATUS:     "-1=UNKNOWN, 0=INVALID, 1=FREERUN, 2=LOCKED, 3=LOCKED_HO_ACQ, 4=HOLDOVER",
	FREQUENCY_STATUS: "-1=UNKNOWN, 0=INVALID, 1=FREERUN, 2=LOCKED, 3=LOCKED_HO_ACQ, 4=HOLDOVER",
	NMEA_STATUS:      "0 = UNAVAILABLE, 1 = AVAILABLE",
	PPS_STATUS:       "0 = UNAVAILABLE, 1 = AVAILABLE",
}

// ClockType ...
type ClockType string

// ClockClassRequest ...
type ClockClassRequest struct {
	cfgName       string
	clockState    PTPState
	clockType     ClockType
	clockClass    fbprotocol.ClockClass
	clockAccuracy fbprotocol.ClockAccuracy
}

var (
	//  make sure only one clock class update is tried if it fails next  try will pass
	// this will also stop flooding
	clockClassRequestCh = make(chan ClockClassRequest, 1)
)

const (
	// GM ..
	GM ClockType = "GM"
	// BC ...
	BC ClockType = "BC"
	// OC ...
	OC ClockType = "OC"
	// ClockUnset ...
	ClockUnset ClockType = ""
)

// PTP4lProcessName ...
const PTP4lProcessName = "ptp4l"

// TS2PHCProcessName ...
const TS2PHCProcessName = "ts2phc"

// SYNCEProcessName ...
const SYNCEProcessName = "synce"

// EventSource ...
type EventSource string

const (
	GNSS       EventSource = "gnss"
	DPLL       EventSource = "dpll"
	TS2PHC     EventSource = "ts2phc"
	PTP4l      EventSource = "ptp4l"
	PHC2SYS    EventSource = "phc2sys"
	PPS        EventSource = "1pps"
	SYNCE      EventSource = "synce4l"
	MONITORING EventSource = "monitoring"
)

// PTPState ...
type PTPState string

// Summary of States:
// State	Description	Action Taken	Synchronization Status
// S0	Unlocked	The clock is not synchronized to any source	Free-running, no sync
// S1	Clock Step	A large time step is applied to synchronize	Large time offset detected, step adjustment made
// S2/s3	Locked	The clock is synchronized and making small frequency adjustments to stay aligned	Synchronized, making small adjustments
const (

	// PTP_FREERUN ...
	PTP_FREERUN PTPState = "s0"
	// PTP_HOLDOVER ...
	PTP_HOLDOVER PTPState = "s1"
	// PTP_LOCKED ...
	PTP_LOCKED PTPState = "s2"
	// PTP_UNKNOWN
	PTP_UNKNOWN PTPState = "-1"
	// PTP_NOTSET
	PTP_NOTSET PTPState = "-2"
)

const (
	// socketDialTimeout is the maximum time to wait for a single dial attempt to the event socket.
	socketDialTimeout = 5 * time.Second
	// socketWriteTimeout is the maximum time to wait for a write to the event socket.
	socketWriteTimeout = 5 * time.Second
)

type clockSyncState struct {
	state          PTPState
	clockClass     fbprotocol.ClockClass
	sourceLost     bool
	clkLog         string
	lastLoggedTime int64
	leadingIFace   string
	clockAccuracy  fbprotocol.ClockAccuracy
	clockOffset    int64
}

// EventHandler ... event handler to process events
type EventHandler struct {
	sync.Mutex
	nodeName           string
	stdoutSocket       string
	stdoutToSocket     bool
	processChannel     <-chan Event
	closeCh            chan bool
	conn               net.Conn   // event socket connection, guarded by connMu
	connMu             sync.Mutex // separate mutex for conn to avoid deadlocks with embedded sync.Mutex
	reconnectMu        sync.Mutex // serializes reconnection attempts to prevent leaked connections
	data               map[string][]*Data
	offsetMetric       *prometheus.GaugeVec
	clockMetric        *prometheus.GaugeVec
	clockClassMetric   *prometheus.GaugeVec
	clockClass         fbprotocol.ClockClass
	clockAccuracy      fbprotocol.ClockAccuracy
	clkSyncState       map[string]*clockSyncState
	downstreamCancel   map[string]context.CancelFunc // cancels in-flight downstream update goroutines per config
	outOfSpec          bool                          // is offset out of spec, used for Lost Source,In Spec and OPut of Spec state transitions
	frequencyTraceable bool                          // will be tru if synce is traceable
	ReduceLog          bool                          // reduce logs for every announce
	LeadingClockData   *LeadingClockParams
	portRole           map[string]map[string]*parser.PTPEvent
}

// getConn returns the current event socket connection under lock.
func (e *EventHandler) getConn() net.Conn {
	e.connMu.Lock()
	defer e.connMu.Unlock()
	return e.conn
}

// setConn replaces the current event socket connection under lock, closing the previous one if it exists.
func (e *EventHandler) setConn(c net.Conn) {
	e.connMu.Lock()
	oldConn := e.conn
	e.conn = c
	e.connMu.Unlock()
	if oldConn != nil && oldConn != c {
		if err := oldConn.Close(); err != nil {
			glog.Warningf("failed to close old event handler connection: %v", err)
		}
	}
}

// EventData is the sealed interface for type-specific event payloads.
type EventData interface{ eventData() } //nolint:revive // "Data" conflicts with stats.go Data struct

// GNSSData carries GNSS receiver status. It does not use PTPState.
type GNSSData struct {
	GPSStatus  int64
	Offset     int64
	SourceLost bool // GPS fix lost (status < 3) or offset out of range
}

func (*GNSSData) eventData() {}

// PTPData carries PTP synchronization status (DPLL, ts2phc, ptp4l, SyncE).
type PTPData struct {
	State              PTPState
	Values             map[ValueType]interface{}
	OutOfSpec          bool
	SourceLost         bool
	FrequencyTraceable bool
}

func (*PTPData) eventData() {}

// Event carries a process event on the event channel.
// Common fields are inline; type-specific data is in Data.
type Event struct {
	Source     EventSource // ptp4l, gnss, dpll, etc.
	IFace      string      // interface that is causing the event
	CfgName    string      // ptp config profile name
	ClockType  ClockType   // oc bc gm
	Time       int64       // time.Now().UnixMilli()
	WriteToLog bool
	Reset      bool      // reset data on ptp deletes or process died
	Data       EventData // *GNSSData or *PTPData; nil for reset events
}

// Init ... initialize event manager
func Init(nodeName string, stdOutToSocket bool, socketName string, processChannel chan Event, closeCh chan bool,
	offsetMetric *prometheus.GaugeVec, clockMetric *prometheus.GaugeVec, clockClassMetric *prometheus.GaugeVec) *EventHandler {
	return &EventHandler{
		nodeName:           nodeName,
		stdoutSocket:       socketName,
		stdoutToSocket:     stdOutToSocket,
		closeCh:            closeCh,
		processChannel:     processChannel,
		data:               map[string][]*Data{},
		clockMetric:        clockMetric,
		offsetMetric:       offsetMetric,
		clockClassMetric:   clockClassMetric,
		clockClass:         protocol.ClockClassUninitialized,
		clkSyncState:       map[string]*clockSyncState{},
		downstreamCancel:   map[string]context.CancelFunc{},
		outOfSpec:          false,
		frequencyTraceable: false,
		ReduceLog:          true,
		LeadingClockData:   newLeadingClockParams(),
		portRole:           map[string]map[string]*parser.PTPEvent{},
	}
}

// GetLogData returns a formatted log line for the event.
func (e *Event) GetLogData() string {
	switch d := e.Data.(type) {
	case *GNSSData:
		state := PTP_FREERUN
		if d.GPSStatus >= 3 && !d.SourceLost {
			state = PTP_LOCKED
		}
		return fmt.Sprintf("%s[%d]:[%s] %s %s %d %s %d %s\n", e.Source,
			time.Now().Unix(), e.CfgName, e.IFace,
			GPS_STATUS, d.GPSStatus, OFFSET, d.Offset, state)
	case *PTPData:
		return formatPTPLogData(e.Source, e.CfgName, e.IFace, d.State, d.Values)
	default:
		return fmt.Sprintf("%s[%d]:[%s] %s\n", e.Source,
			time.Now().Unix(), e.CfgName, e.IFace)
	}
}

func formatPTPLogData(source EventSource, cfgName, iface string, state PTPState, values map[ValueType]interface{}) string {
	logData := make([]string, 0, len(values))
	for k, v := range values {
		switch val := v.(type) {
		case int64, int, int32:
			logData = append(logData, fmt.Sprintf("%s %d", k, val))
		case float64:
			logData = append(logData, fmt.Sprintf("%s %f", k, val))
		case string:
			logData = append(logData, fmt.Sprintf("%s %s", k, val))
		case byte:
			logData = append(logData, fmt.Sprintf("%s %#x", k, val))
		default:
			continue
		}
	}
	sort.Strings(logData)
	return fmt.Sprintf("%s[%d]:[%s] %s %s %s\n", source,
		time.Now().Unix(), cfgName, iface, strings.Join(logData, " "), state)
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
func (e *EventHandler) updateGMState(cfgName string) clockSyncState {
	dpllState := PTP_NOTSET
	gnssState := PTP_FREERUN
	ts2phcState := PTP_FREERUN
	syncSrcLost := e.isSourceLost(cfgName)
	leadingInterface := e.getLeadingInterface(cfgName)
	if leadingInterface == LEADING_INTERFACE_UNKNOWN {
		glog.Infof("Leading interface is not yet identified, clock state reporting delayed.")
		return clockSyncState{leadingIFace: leadingInterface}
	}

	if _, ok := e.clkSyncState[cfgName]; !ok {
		e.clkSyncState[cfgName] = &clockSyncState{
			state:         PTP_FREERUN,
			clockClass:    protocol.ClockClassUninitialized,
			clockAccuracy: fbprotocol.ClockAccuracyUnknown,
			sourceLost:    syncSrcLost,
			leadingIFace:  leadingInterface,
		}
	}
	// right now if GPS offset || mode is bad then consider source lost
	e.clkSyncState[cfgName].sourceLost = syncSrcLost
	e.clkSyncState[cfgName].leadingIFace = leadingInterface
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			switch d.ProcessName {
			case DPLL:
				dpllState = d.State
				if e.hasNonLeadingDPLLFault(cfgName, leadingInterface) {
					dpllState = PTP_FREERUN
				}
			case GNSS:
				gnssState = d.State
				// expecting to have at least one interface
			case TS2PHCProcessName:
				ts2phcState = d.State
				if parser.NoSourceTSCount == 2 {
					ts2phcState = PTP_FREERUN
				}
			}
		}
	} else {
		e.clkSyncState[cfgName].state = PTP_FREERUN
		e.clkSyncState[cfgName].clockClass = protocol.ClockClassFreerun
		e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyUnknown
		e.clkSyncState[cfgName].lastLoggedTime = time.Now().Unix()
		e.clkSyncState[cfgName].leadingIFace = leadingInterface
		e.clkSyncState[cfgName].clkLog = fmt.Sprintf("%s[%d]:[%s] %s T-GM-STATUS %s\n", GM, e.clkSyncState[cfgName].lastLoggedTime, cfgName, leadingInterface, e.clkSyncState[cfgName].state)
		return *e.clkSyncState[cfgName]
	}
	e.clkSyncState[cfgName].leadingIFace = leadingInterface
	switch dpllState {
	case PTP_FREERUN: // This is OVER ALL State with HOLDOVER having the highest priority
		// add check so that clock class won't change if GM was in HOLDOVER state
		e.clkSyncState[cfgName].state = dpllState
		// T-GM or T-BC in free-run mode
		if e.outOfSpec && e.frequencyTraceable {
			// T-GM in holdover, out of holdover specification
			e.clkSyncState[cfgName].clockClass = protocol.ClockClassOutOfSpec
		} else { // from holdover it goes to out of spec to free run
			// T-GM or T-BC in free-run mode
			e.clkSyncState[cfgName].clockClass = protocol.ClockClassFreerun
		}
		e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyUnknown
	case PTP_HOLDOVER:
		e.clkSyncState[cfgName].state = dpllState
		// T-GM in holdover, within holdover specification
		e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass7
	case PTP_LOCKED, PTP_NOTSET: // consider DPLL is locked if DPLL is not available
		switch gnssState {
		case PTP_LOCKED:
			switch ts2phcState {
			case PTP_FREERUN:
				e.clkSyncState[cfgName].state = PTP_FREERUN
				// T-GM or T-BC in free-run mode
				e.clkSyncState[cfgName].clockClass = protocol.ClockClassFreerun
				e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyUnknown
			case PTP_LOCKED:
				e.clkSyncState[cfgName].state = PTP_LOCKED
				// T-GM connected to a PRTC in locked mode (e.g., PRTC traceable to GNSS)
				e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass6
				e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			case PTP_HOLDOVER:
				e.clkSyncState[cfgName].state = PTP_HOLDOVER
				e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass7
			}
		case PTP_FREERUN:
			if syncSrcLost {
				switch ts2phcState {
				case PTP_LOCKED:
				case PTP_FREERUN:
					e.clkSyncState[cfgName].state = PTP_FREERUN
				// stay with last GM state and wait for DPLL to move to HOLDOVER
				case PTP_HOLDOVER:
					e.clkSyncState[cfgName].state = PTP_HOLDOVER
					e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass7
				}
			} else {
				switch ts2phcState {
				case PTP_FREERUN, PTP_LOCKED, PTP_UNKNOWN, PTP_NOTSET:
					e.clkSyncState[cfgName].state = PTP_FREERUN
					// T-GM or T-BC in free-run mode
					e.clkSyncState[cfgName].clockClass = protocol.ClockClassFreerun
					e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyUnknown
				}
			}
		}
	default:
		switch gnssState {
		case PTP_LOCKED:
			switch ts2phcState {
			case PTP_FREERUN, PTP_UNKNOWN, PTP_NOTSET:
				e.clkSyncState[cfgName].state = PTP_FREERUN
				// T-GM or T-BC in free-run mode
				e.clkSyncState[cfgName].clockClass = protocol.ClockClassFreerun
				e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyUnknown
			case PTP_LOCKED:
				e.clkSyncState[cfgName].state = PTP_LOCKED
				// T-GM connected to a PRTC in locked mode (e.g., PRTC traceable to GNSS)
				e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass6
				e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			case PTP_HOLDOVER:
				e.clkSyncState[cfgName].state = PTP_HOLDOVER
				e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass7 //TODO: check if this is correct
			}
		case PTP_FREERUN:
			switch ts2phcState {
			case PTP_FREERUN, PTP_LOCKED, PTP_UNKNOWN, PTP_NOTSET: // when GNSS is lost ts2phc will stop printing and will wait to move to HOLDOVER
				e.clkSyncState[cfgName].state = PTP_FREERUN
				e.clkSyncState[cfgName].clockClass = protocol.ClockClassFreerun
				e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyUnknown
			case PTP_HOLDOVER: // if holdover is detected then wait for ts2phc to move to HOLDOVER
				e.clkSyncState[cfgName].state = PTP_HOLDOVER
				e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass7 //TODO: check if this is correct
			}
		default: // bad case
			e.clkSyncState[cfgName].state = ts2phcState
			switch ts2phcState {
			case PTP_FREERUN:
				e.clkSyncState[cfgName].clockClass = protocol.ClockClassFreerun
				e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyUnknown
			case PTP_LOCKED:
				e.clkSyncState[cfgName].clockClass = fbprotocol.ClockClass7
				e.clkSyncState[cfgName].clockAccuracy = fbprotocol.ClockAccuracyNanosecond100
			}
		}
	}
	gSycState := e.clkSyncState[cfgName]
	rclockSyncState := clockSyncState{
		state:         gSycState.state,
		clockClass:    gSycState.clockClass,
		clockAccuracy: gSycState.clockAccuracy,
		sourceLost:    gSycState.sourceLost,
		leadingIFace:  gSycState.leadingIFace,
	}
	// this will reduce log noise and prints 1 per sec
	logTime := time.Now().Unix()
	if e.clkSyncState[cfgName].lastLoggedTime != logTime {
		clkLog := fmt.Sprintf("%s[%d]:[%s] %s T-GM-STATUS %s\n", GM, logTime, cfgName, gSycState.leadingIFace, gSycState.state)
		e.clkSyncState[cfgName].lastLoggedTime = logTime
		e.clkSyncState[cfgName].clkLog = clkLog
		rclockSyncState.clkLog = clkLog
		glog.Infof("dpll State %s, gnss State %s, tsphc state %s, gm state %s,", dpllState, gnssState, ts2phcState, e.clkSyncState[cfgName].state)
	}
	return rclockSyncState
}

func (e *EventHandler) getGMClockClass(cfgName string) fbprotocol.ClockClass {
	if g, ok := e.clkSyncState[cfgName]; ok {
		return g.clockClass
	}
	return protocol.ClockClassUninitialized
}

func (e *EventHandler) isSourceLost(cfgName string) bool {
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			if d.ProcessName == GNSS && len(d.Details) > 0 && d.Details[0] != nil {
				return d.Details[0].sourceLost
			}
		}
	}
	return false
}

// hasNonLeadingDPLLFault returns true when the leading DPLL is locked but at
// least one non-leading DPLL is not locked, indicating a follower fault that
// should force the composite clock to FREERUN.
func (e *EventHandler) hasNonLeadingDPLLFault(cfgName, leadingInterface string) bool {
	if leadingInterface == LEADING_INTERFACE_UNKNOWN {
		return false
	}
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			if d.ProcessName != DPLL {
				continue
			}
			leadingDetail := d.GetDataDetails(leadingInterface)
			if leadingDetail == nil || leadingDetail.State != PTP_LOCKED {
				return false
			}
			for _, dd := range d.Details {
				if dd.IFace != leadingInterface && dd.State != PTP_LOCKED {
					glog.Infof("non-leading DPLL %s is %s while leading %s is locked, composite DPLL forced to FREERUN",
						dd.IFace, dd.State, leadingInterface)
					return true
				}
			}
		}
	}
	return false
}

func (e *EventHandler) getLeadingInterface(cfgName string) string {
	if data, ok := e.data[cfgName]; ok {
		for _, d := range data {
			if d.ProcessName == GNSS && len(d.Details) > 0 {
				return d.Details[0].IFace
			} else if d.ProcessName == TS2PHCProcessName && len(d.Details) > 0 {
				for _, dd := range d.Details {
					if dd.signalSource == GNSS {
						return dd.IFace
					}
				}
			}
		}
	}
	return LEADING_INTERFACE_UNKNOWN
}

func (e *EventHandler) updateSpecState(event Event) {
	if ptp, ok := event.Data.(*PTPData); ok && event.Source == DPLL {
		e.outOfSpec = ptp.OutOfSpec
		e.frequencyTraceable = ptp.FrequencyTraceable
	}
}
func (e *EventHandler) toString() string {
	// update if DPLL holdover is out of spec
	out := strings.Builder{}
	for cfgName, eData := range e.data {
		out.WriteString("  data key : " + string(cfgName) + "\r\n")
		for _, data := range eData {
			out.WriteString("  state: " + string(data.State) + "\r\n")
			out.WriteString("  process name: " + string(data.ProcessName) + "\r\n")
			for _, dataDetails := range data.Details {
				for mn, mv := range dataDetails.Metrics {
					out.WriteString("  metric key: " + string(mn) + "\r\n")
					out.WriteString("  metric Name: " + mv.Name + "\r\n")
					out.WriteString("  registered: " + strconv.FormatBool(mv.isRegistered) + "\r\n")
				}
				out.WriteString("  signal source: " + string(dataDetails.signalSource) + "\r\n")
				out.WriteString("  details state: " + string(dataDetails.State) + "\r\n")
				out.WriteString("  log: " + string(dataDetails.logData) + "\r\n")
				out.WriteString("  iface: " + string(dataDetails.IFace) + "\r\n")
				out.WriteString("  source lost : " + strconv.FormatBool(dataDetails.sourceLost) + "\r\n")
			}
			out.WriteString("-----\r\n")
		}
	}
	return out.String()
}

func (e *EventHandler) hasMetric(name string) (*prometheus.GaugeVec, bool) {
	// update if DPLL holdover is out of spec
	for _, eData := range e.data {
		for _, data := range eData {
			for _, dataDetails := range data.Details {
				for _, mv := range dataDetails.Metrics {
					if mv.Name == name {
						return mv.GaugeMetric, true
					}
				}
			}
		}
	}
	return nil, false
}

// AnnounceClockClass announces clock class changes to the event handler and writes to the connection.
// It also sends a non-blocking clock class update request to the ProcessEvents goroutine,
// which calls UpdateClockClass to read the local GRANDMASTER_SETTINGS_NP and determine
// the correct clock class for the local clock (e.g., 255 for OC slave).
func (e *EventHandler) AnnounceClockClass(clockClass fbprotocol.ClockClass, clockAcc fbprotocol.ClockAccuracy, cfgName string, clockType ClockType) {
	e.announceClockClass(clockClass, clockAcc, cfgName)
	// Non-blocking send to trigger UpdateClockClass in the ProcessEvents goroutine.
	// For non-GM clock types (OC/BC), UpdateClockClass reads the local GRANDMASTER_SETTINGS_NP
	// to determine the correct clock class (e.g., 255 for OC slave).
	select {
	case clockClassRequestCh <- ClockClassRequest{
		cfgName:       cfgName,
		clockClass:    clockClass,
		clockType:     clockType,
		clockAccuracy: clockAcc,
	}:
	default:
		glog.Warning("clock class request busy updating previous request, will try on next event")
	}
}

func (e *EventHandler) announceClockClass(clockClass fbprotocol.ClockClass, clockAcc fbprotocol.ClockAccuracy, cfgName string) {
	e.Lock()
	e.setClockClassLocked(clockClass, clockAcc)
	e.storeClockClassLocked(cfgName, clockClass, clockAcc)
	e.Unlock()

	e.emitClockClass(clockClass, cfgName)
}

// setClockClassLocked updates the clock class and accuracy fields.
// Caller must hold e.Lock().
func (e *EventHandler) setClockClassLocked(clockClass fbprotocol.ClockClass, clockAcc fbprotocol.ClockAccuracy) {
	e.clockClass = clockClass
	e.clockAccuracy = clockAcc
}

// storeClockClassLocked stores the clock class and accuracy in clkSyncState
// so that EmitClockClass and the classTicker can re-emit after a reconnect.
// Caller must hold e.Lock().
func (e *EventHandler) storeClockClassLocked(cfgName string, clockClass fbprotocol.ClockClass, clockAcc fbprotocol.ClockAccuracy) {
	if _, ok := e.clkSyncState[cfgName]; !ok {
		e.clkSyncState[cfgName] = &clockSyncState{}
	}
	e.clkSyncState[cfgName].clockClass = clockClass
	e.clkSyncState[cfgName].clockAccuracy = clockAcc
}

// emitClockClass writes the clock class to the socket and updates the metric.
// Must NOT be called while holding e.Lock().
func (e *EventHandler) emitClockClass(clockClass fbprotocol.ClockClass, cfgName string) {
	if e.stdoutToSocket {
		logMsg := utils.GetClockClassLogMessage(PTP4lProcessName, cfgName, clockClass)
		e.writeLogToSocket(logMsg)
	}
	if !e.stdoutToSocket && e.clockClassMetric != nil {
		e.clockClassMetric.With(prometheus.Labels{
			"process": PTP4lProcessName, "config": cfgName, "node": e.nodeName}).Set(float64(clockClass))
	}
}

// reconnectEventSocket closes the current connection and dials a new one using
// the shared reconnection utility with exponential backoff.
// Serialized via reconnectMu to prevent concurrent reconnection attempts from leaking connections.
// On success, stores the new connection via e.setConn and returns true.
// Returns false if the handler is shutting down or all retries are exhausted.
func (e *EventHandler) reconnectEventSocket() bool {
	e.reconnectMu.Lock()
	defer e.reconnectMu.Unlock()

	// Another goroutine may have already reconnected while we were waiting for the lock.
	if e.getConn() != nil {
		return true
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-e.closeCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer cancel()
	dialer := net.Dialer{Timeout: socketDialTimeout}
	newConn := utils.ReconnectWithBackoff(ctx,
		func() (net.Conn, error) { return dialer.DialContext(ctx, "unix", e.stdoutSocket) },
		utils.DefaultReconnectConfig(),
	)
	if newConn != nil {
		e.setConn(newConn)
		return true
	}
	return false
}

// writeLogToSocket writes a single log line to the event socket.
// If the write fails, it attempts to reconnect and retry once.
// Returns true if the connection is still usable for subsequent writes,
// false if the connection is unavailable and remaining writes should be skipped.
func (e *EventHandler) writeLogToSocket(l string) bool {
	if !strings.HasSuffix(l, "\n") {
		l += "\n"
	}
	conn := e.getConn()
	if conn == nil {
		if !e.reconnectEventSocket() {
			glog.Warning("Connection is nil and reconnect failed, skipping socket write")
			return false
		}
		conn = e.getConn()
		if conn == nil {
			glog.Error("Connection is still nil after successful reconnect, skipping socket write")
			return false
		}
	}
	if err := conn.SetWriteDeadline(time.Now().Add(socketWriteTimeout)); err != nil {
		glog.Warningf("Failed to set write deadline: %v", err)
	}
	if _, err := conn.Write([]byte(l)); err != nil {
		glog.Errorf("Write error for %q: %v", l, err)
		// Clear the broken connection before reconnecting so that
		// concurrent callers waiting on reconnectMu see conn==nil
		// and don't mistakenly return the broken connection.
		e.setConn(nil)
		if !e.reconnectEventSocket() {
			glog.Warning("Reconnect failed after write error, skipping remaining socket writes; will retry on next event")
			return false
		}
		// Retry write on the new connection
		retryConn := e.getConn()
		if retryConn == nil {
			glog.Warning("Connection is nil after reconnect, skipping retry")
			return false
		}
		if deadlineErr := retryConn.SetWriteDeadline(time.Now().Add(socketWriteTimeout)); deadlineErr != nil {
			glog.Warningf("Failed to set write deadline on retry: %v", deadlineErr)
		}
		if _, retryErr := retryConn.Write([]byte(l)); retryErr != nil {
			glog.Errorf("Write failed again after reconnect for %q: %v", l, retryErr)
			e.setConn(nil)
			return false
		}
	}
	return true
}

// ProcessEvents ... process events to generate new events
func (e *EventHandler) ProcessEvents() {
	redialClockClass := true

	defer func() {
		if e.stdoutToSocket {
			e.setConn(nil) // closes the connection if present
		}
	}()
	var lastClockState PTPState

	// Establish initial connection to the event socket using exponential backoff.
	// Retries indefinitely until connected or the handler is shutting down.
	if e.stdoutToSocket {
		for !e.reconnectEventSocket() {
			// reconnectEventSocket returns false on shutdown or exhausted retries;
			// check for shutdown before retrying
			select {
			case <-e.closeCh:
				return
			default:
				glog.Warning("Initial connection to event socket failed, retrying in 1 second...")
				time.Sleep(1 * time.Second)
			}
		}
	}

	if redialClockClass {
		go func() {
			defer func() {
				if err := recover(); err != nil {
					glog.Errorf("restored from clock class update: %s", err)
				}
			}()
			cfgName := ""
			classTicker := time.NewTicker(60 * time.Second)
			for {
				select {
				case clk := <-clockClassRequestCh:
					cfgName = clk.cfgName
					// TODO: UpdateClockClass produces the wrong value for BC, investigate and fix.
					if clk.clockType != BC {
						e.UpdateClockClass(clk)
					} else {
						e.Lock()
						e.clockClass = clk.clockClass
						e.clockAccuracy = clk.clockAccuracy
						e.storeClockClassLocked(clk.cfgName, clk.clockClass, clk.clockAccuracy)
						e.Unlock()
					}

				case <-e.closeCh:
					return
				case <-classTicker.C: // send clock class event 60 secs interval
					// Snapshot the clock sync state under lock to avoid concurrent map access
					e.Lock()
					clkSnapshot := make(map[string]fbprotocol.ClockClass, len(e.clkSyncState))
					for k, v := range e.clkSyncState {
						clkSnapshot[k] = v.clockClass
					}
					e.Unlock()
					for clkCfgName, clockClass := range clkSnapshot {
						parts := strings.SplitN(clkCfgName, ".", 2)
						if len(parts) >= 2 {
							clkCfgName = "ptp4l." + strings.Join(parts[1:], ".")
						}
						if clockClass == 0 {
							continue
						}
						if clkCfgName == cfgName {
							// Stop double emmit
							cfgName = ""
						}
						logMsg := utils.GetClockClassLogMessage(PTP4lProcessName, clkCfgName, clockClass)
						if !e.writeLogToSocket(logMsg) {
							break
						}
					}

					if cfgName != "" {
						parts := strings.SplitN(cfgName, ".", 2)
						if len(parts) >= 2 {
							cfgName = "ptp4l." + strings.Join(parts[1:], ".")
						}
						e.Lock()
						currentClockClass := e.clockClass
						e.Unlock()
						logMsg := utils.GetClockClassLogMessage(PTP4lProcessName, cfgName, currentClockClass)
						e.writeLogToSocket(logMsg)
					}
				}
			}
		}()
		redialClockClass = false
	}
	glog.Info("starting state monitoring...")
	for {
		select {
		case event := <-e.processChannel: // for non GM this thread will be in sleep forever
			// ts2phc[123455]:[ts2phc.0.config] 12345 s0 offset/gps
			// replace ts2phc logs here
			if event.Reset { // clean up
				debug.ClearState() // clear any state data used for debug
				e.LeadingClockData = newLeadingClockParams()
				if event.Source == TS2PHC {
					e.unregisterMetrics(event.CfgName, "")
					delete(e.data, event.CfgName) // this will delete all index
					e.clockClass = protocol.ClockClassUninitialized
					e.clockAccuracy = fbprotocol.ClockAccuracyUnknown
				} else {
					// Check if the index is within the slice bounds
					for indexToRemove, d := range e.data[event.CfgName] {
						if d.ProcessName == event.Source {
							e.unregisterMetrics(event.CfgName, string(event.Source))
							if indexToRemove < len(e.data[event.CfgName]) {
								e.data[event.CfgName] = append(e.data[event.CfgName][:indexToRemove], e.data[event.CfgName][indexToRemove+1:]...)
							}
						}
					}
					e.Lock()
					delete(e.clkSyncState, event.CfgName) // delete the clkSyncState
					e.Unlock()
					e.outOfSpec = false
					e.frequencyTraceable = false
				}
				continue
			}
			var logOut []string
			logDataValues := ""
			if event.Source == SYNCE {
				ptp, _ := event.Data.(*PTPData)
				logDataValues = event.GetLogData()
				if event.WriteToLog && logDataValues != "" {
					logOut = append(logOut, logDataValues)
				}
				if !e.stdoutToSocket && ptp != nil {
					e.UpdateClockStateMetrics(ptp.State, string(event.Source), event.IFace)
				}
			} else {

				// Update the in MemData

				var clockState clockSyncState
				var dataDetails *DataDetails
				if event.ClockType == GM {
					dataDetails = e.addEvent(event)
					// Computes GM state
					e.Lock()
					clockState = e.updateGMState(event.CfgName)
					e.Unlock()
					if clockState.state != PTP_LOCKED {
						if ptp, isPTP := event.Data.(*PTPData); isPTP {
							if _, hasNMEA := ptp.Values[NMEA_STATUS]; hasNMEA {
								ptp.Values[NMEA_STATUS] = 0
							}
						}
					}
				} else { // T-BC or T-TSC
					e.Lock()
					event = e.convergeConfig(event)
					dataDetails = e.addEvent(event)
					var needsTTSCAnnounce, needsDownstreamUpdate bool
					clockState, needsTTSCAnnounce, needsDownstreamUpdate = e.updateBCState(event)
					e.Unlock()
					// Perform I/O after releasing the lock
					if needsTTSCAnnounce {
						e.emitClockClass(clockState.clockClass, event.CfgName)
					}
					if needsDownstreamUpdate {
						go e.updateDownstreamData(event.CfgName)
					}
				}
				logDataValues = dataDetails.logData
				if event.WriteToLog && logDataValues != "" {
					logOut = append(logOut, logDataValues)
				}
				d := e.GetData(event.CfgName, event.Source)

				switch data := event.Data.(type) {
				case *GNSSData:
					debug.UpdateGNSSState(string(d.State), data.Offset)
				case *PTPData:
					switch event.Source {
					case DPLL:
						debug.UpdateDPLLState(string(data.State), data.Values[OFFSET], event.IFace)
						debug.UpdateDPLLState(string(d.State), 0, debug.OverallDpllKey)
					case TS2PHC:
						debug.UpdateTs2phcState(string(data.State), data.Values[OFFSET], event.IFace)
						debug.UpdateTs2phcState(string(d.State), 0, debug.OverallTs2phcKey)
					}
				}
				debug.UpdateGMState(string(clockState.state))

				if clockState.clkLog != "" && clockState.leadingIFace != LEADING_INTERFACE_UNKNOWN {
					logOut = append(logOut, clockState.clkLog)
				}

				// Update the metrics
				if !e.stdoutToSocket {
					switch data := event.Data.(type) {
					case *GNSSData:
						e.UpdateClockStateMetrics(d.State, string(event.Source), alias.GetAlias(event.IFace))
						gnssValues := map[ValueType]interface{}{
							GPS_STATUS: data.GPSStatus,
							OFFSET:     data.Offset,
						}
						e.updateMetrics(event.CfgName, event.Source, gnssValues, dataDetails)
					case *PTPData:
						e.UpdateClockStateMetrics(data.State, string(event.Source), alias.GetAlias(event.IFace))
						e.updateMetrics(event.CfgName, event.Source, data.Values, dataDetails)
					}
					if clockState.leadingIFace != LEADING_INTERFACE_UNKNOWN {
						e.UpdateClockStateMetrics(clockState.state, string(event.ClockType), alias.GetAlias(clockState.leadingIFace))
					}
				}
				if event.ClockType == GM {
					clockState.clockAccuracy = e.clockAccuracy

					if ptp, isPTP := event.Data.(*PTPData); isPTP && event.Source == DPLL {
						if clockState.clockClass == fbprotocol.ClockClass7 || clockState.clockClass == protocol.ClockClassOutOfSpec {
							if offset, found := ptp.Values[OFFSET]; found {
								offsetValue, isInt64 := offset.(int64)
								if isInt64 {
									clockAccuracy := fbprotocol.ClockAccuracyFromOffset(time.Duration(offsetValue) * time.Nanosecond)
									clockState.clockAccuracy = clockAccuracy
								}
							}
						}
					}

					if clockState.clockClass != protocol.ClockClassUninitialized &&
						(clockState.clockClass != e.clockClass || clockState.clockAccuracy != e.clockAccuracy) {
						glog.Infof("clock class change request from %d to %d with clock accuracy from %d to %d",
							uint8(e.clockClass), uint8(clockState.clockClass), uint8(e.clockAccuracy), uint8(clockState.clockAccuracy))
						debug.UpdateClockClass(uint8(clockState.clockClass))
						go func() {
							select {
							case clockClassRequestCh <- ClockClassRequest{
								cfgName:       event.CfgName,
								clockState:    clockState.state,
								clockType:     event.ClockType,
								clockClass:    clockState.clockClass,
								clockAccuracy: clockState.clockAccuracy,
							}:
							default:
								glog.Error("clock class request busy updating previous request, will try next event")
							}
						}()
					}
					if lastClockState != clockState.state {
						glog.Infof("PTP State: %v, Clock Class %d Time %s sourceLost %v", clockState.state, clockState.clockClass, time.Now(), clockState.sourceLost)
						lastClockState = clockState.state
					}
				} // T-GM
			} // Not SYNC-E

			if len(logOut) > 0 {
				// Always print all logs to stdout regardless of socket state
				for _, l := range logOut {
					fmt.Printf("%s", l)
				}
				if e.stdoutToSocket {
					if e.getConn() == nil {
						glog.Error("No connection available, attempting reconnect")
						if !e.reconnectEventSocket() {
							glog.Warning("Reconnect failed, skipping socket writes; will retry on next event")
						}
					}
					for _, l := range logOut {
						if !e.writeLogToSocket(l) {
							break
						}
					}
				}
			}
		case <-e.closeCh:
			return
		}
	}
}

func (e *EventHandler) updateClockClass(cfgName string, clkClass fbprotocol.ClockClass, clockType ClockType, clkAccuracy fbprotocol.ClockAccuracy,
	gmGetterFn func(string) (protocol.GrandmasterSettings, error),
	gmSetterFn func(string, protocol.GrandmasterSettings) error) (err error, clockClass fbprotocol.ClockClass, clockAccuracy fbprotocol.ClockAccuracy) {
	g, err := gmGetterFn(cfgName)
	if err != nil {
		glog.Errorf("failed to get current GRANDMASTER_SETTINGS_NP: %s", err)
		return err, clockClass, clockAccuracy
	}
	switch clockType {
	case GM:
		g.TimePropertiesDS.PtpTimescale = true
		g.TimePropertiesDS.FrequencyTraceable = true
		g.TimePropertiesDS.CurrentUtcOffsetValid = true
		g.TimePropertiesDS.CurrentUtcOffset = int32(leap.GetUtcOffset())
		switch clkClass {
		case fbprotocol.ClockClass6: // T-GM connected to a PRTC in locked mode (e.g., PRTC traceable to GNSS)
			// update only when ClockClass is changed or clockAccuracy changes
			if g.ClockQuality.ClockClass != fbprotocol.ClockClass6 || g.TimePropertiesDS.TimeTraceable != true {
				g.ClockQuality.ClockClass = fbprotocol.ClockClass6
				g.TimePropertiesDS.TimeTraceable = true
				g.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
				g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceGNSS
				// T-REC-G.8275.1-202211-I section 6.3.5
				g.ClockQuality.OffsetScaledLogVariance = 0x4e5d
				err = gmSetterFn(cfgName, g)
			}
		case protocol.ClockClassOutOfSpec: // GM out of holdover specification, traceable to Category 3
			if g.ClockQuality.ClockClass != protocol.ClockClassOutOfSpec {
				g.ClockQuality.ClockClass = protocol.ClockClassOutOfSpec
				g.TimePropertiesDS.TimeTraceable = false
				g.ClockQuality.ClockAccuracy = clkAccuracy
				g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceInternalOscillator
				// T-REC-G.8275.1-202211-I section 6.3.5
				g.ClockQuality.OffsetScaledLogVariance = 0xffff
				err = gmSetterFn(cfgName, g)
			}
		case fbprotocol.ClockClass7: // T-GM in holdover, within holdover specification
			if g.ClockQuality.ClockClass != fbprotocol.ClockClass7 {
				g.ClockQuality.ClockClass = fbprotocol.ClockClass7
				g.TimePropertiesDS.TimeTraceable = true
				g.ClockQuality.ClockAccuracy = clkAccuracy
				g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceInternalOscillator
				// T-REC-G.8275.1-202211-I section 6.3.5
				g.ClockQuality.OffsetScaledLogVariance = 0xffff
				err = gmSetterFn(cfgName, g)
			}
		case protocol.ClockClassFreerun: // T-GM in free-run mode
			if g.ClockQuality.ClockClass != protocol.ClockClassFreerun {
				g.ClockQuality.ClockClass = protocol.ClockClassFreerun
				g.TimePropertiesDS.TimeTraceable = false
				g.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
				g.TimePropertiesDS.TimeSource = fbprotocol.TimeSourceInternalOscillator
				// T-REC-G.8275.1-202211-I section 6.3.5
				g.ClockQuality.OffsetScaledLogVariance = 0xffff
				err = gmSetterFn(cfgName, g)
			}
		default:
			glog.Infof("No clock class identified for %d", clkClass)
		}
	default: // other than GM
	}
	return err, g.ClockQuality.ClockClass, g.ClockQuality.ClockAccuracy
}

// GetPTPState ...
func (e *EventHandler) GetPTPState(source EventSource, cfgName string) PTPState {
	if m, ok := e.data[cfgName]; ok {
		for _, v := range m {
			if v.ProcessName == source {
				return v.State
			}
		}
	}
	return PTP_UNKNOWN
}

// UpdateClockStateMetrics ...
func (e *EventHandler) UpdateClockStateMetrics(state PTPState, process, iFace string) {
	if !utils.CheckMetricSanity("ClockState", process, iFace) {
		return
	}
	if e.stdoutToSocket {
		return
	}
	labels := prometheus.Labels{
		"process": process, "node": e.nodeName, "iface": iFace}
	if state == PTP_LOCKED {
		e.clockMetric.With(labels).Set(1)
	} else if state == PTP_FREERUN {
		e.clockMetric.With(labels).Set(0)
	} else if state == PTP_HOLDOVER {
		e.clockMetric.With(labels).Set(2)
	} else {
		e.clockMetric.With(labels).Set(3)
	}
}

func (e *EventHandler) updateMetrics(cfgName string, process EventSource, processData map[ValueType]interface{}, d *DataDetails) {
	iface := alias.GetAlias(d.IFace)

	for dataType, value := range processData { // update process with metrics
		var dataValue float64
		switch val := value.(type) {
		case int64:
			dataValue = float64(val)
		case float64:
			dataValue = val
		default:
			continue //ignore string for metrics
		}

		if _, found := d.Metrics[dataType]; !found {
			if dataType == OFFSET {
				pName := string(process)
				if process == TS2PHCProcessName {
					pName = "master"
				}
				if d.Metrics[dataType].GaugeMetric == nil {
					m := d.Metrics[dataType]
					m.GaugeMetric = e.offsetMetric
					m.isRegistered = true
					d.Metrics[dataType] = m
				}
				pLabels := map[string]string{"from": pName, "node": e.nodeName,
					"process": string(process), "iface": iface}
				d.Metrics[dataType].GaugeMetric.With(pLabels).Set(dataValue)
			} else {
				metric := DataMetric{
					isRegistered: true,
					GaugeMetric: prometheus.NewGaugeVec(
						prometheus.GaugeOpts{
							Namespace: PTPNamespace,
							Subsystem: PTPSubsystem,
							Name:      getMetricName(dataType),
							Help:      valueTypeHelpTxt[dataType],
						}, []string{"from", "node", "process", "iface"}),
					CounterMetric: nil,
					Name:          string(dataType),
					ValueType:     prometheus.GaugeValue,
					Labels: map[string]string{"from": string(process), "node": e.nodeName,
						"process": string(process), "iface": iface},
					Value: dataValue,
				}

				if gaugeMetric, ok := e.hasMetric(getMetricName(dataType)); ok {
					metric.GaugeMetric = gaugeMetric
				} else {
					glog.Infof("trying to register metrics %#v for %s", metric, dataType)
					registerMetrics(metric.GaugeMetric)
				}
				metric.GaugeMetric.With(metric.Labels).Set(dataValue)
				d.Metrics[dataType] = metric
			}
		} else {
			pName := string(process)
			if dataType == OFFSET && process == TS2PHCProcessName {
				pName = "master"
			}
			s := d.Metrics[dataType]
			s.Labels = map[string]string{"from": pName, "node": e.nodeName,
				"process": string(process), "iface": iface}
			s.Value = dataValue
			d.Metrics[dataType].GaugeMetric.With(s.Labels).Set(s.Value)
		}
	}

}

func registerMetrics(m *prometheus.GaugeVec) {
	defer func() {
		if err := recover(); err != nil {
			glog.Errorf("restored from registering metrics: %s", err)
		}
	}()
	prometheus.MustRegister(m)
}

func (e *EventHandler) unregisterMetrics(configName string, processName string) {
	if e.stdoutToSocket {
		return // no need to unregister metrics if events are going to socket
	}
	if data, ok := e.data[configName]; ok {
		for _, v := range data {
			if string(v.ProcessName) == processName || processName == "" {
				for _, d := range v.Details {
					for _, metric := range d.Metrics {
						if metric.GaugeMetric != nil {
							metric.GaugeMetric.Delete(metric.Labels)
						}
					}
				}
			}
		}
	}
}

// GetData returns the queried Data and create one if not exist
func (e *EventHandler) GetData(cfgName string, processName EventSource) *Data {
	if e.data[cfgName] == nil {
		e.data[cfgName] = []*Data{}
	}

	for _, d := range e.data[cfgName] {
		if d.ProcessName == processName {
			return d
		}
	}

	d := &Data{
		ProcessName: processName,
		State:       PTP_UNKNOWN,
		window:      *utils.NewWindow(WindowSize),
	}
	e.data[cfgName] = append(e.data[cfgName], d)
	return d
}

func (e *EventHandler) addEvent(event Event) *DataDetails {
	d := e.GetData(event.CfgName, event.Source)
	d.AddEvent(event)

	// update if DPLL holdover is out of spec
	e.updateSpecState(event)
	d.UpdateState()
	return d.GetDataDetails(event.IFace)
}

// UpdateClockClass ... update clock class
func (e *EventHandler) UpdateClockClass(clk ClockClassRequest) {
	getter := func(cfgName string) (protocol.GrandmasterSettings, error) {
		cfgName = strings.Replace(cfgName, TS2PHCProcessName, PTP4lProcessName, 1)
		return pmc.GetGMSettings(cfgName)
	}
	setter := func(cfgName string, g protocol.GrandmasterSettings) error {
		cfgName = strings.Replace(cfgName, TS2PHCProcessName, PTP4lProcessName, 1)
		if err := pmc.SetGMSettings(cfgName, g); err != nil {
			return fmt.Errorf("failed to update GRANDMASTER_SETTINGS_NP: %s", err)
		}
		return nil
	}
	classErr, clockClass, clockAccuracy := e.updateClockClass(clk.cfgName, clk.clockClass, clk.clockType, clk.clockAccuracy,
		getter, setter)
	glog.Infof("received %s,%v,%s,%v", clk.cfgName, clk.clockClass, clk.clockType, clk.clockAccuracy)
	if classErr != nil {
		glog.Errorf("error updating clock class %s", classErr)
	} else {
		glog.Infof("updated clock class for last clock class %d to %d with clock accuracy %d", clk.clockClass, clockClass, clockAccuracy)
		e.Lock()
		e.clockClass = clockClass
		e.clockAccuracy = clockAccuracy
		e.storeClockClassLocked(clk.cfgName, clockClass, clockAccuracy)
		e.Unlock()
		clockClassOut := utils.GetClockClassLogMessage(PTP4lProcessName, clk.cfgName, clockClass)
		if e.stdoutToSocket {
			e.writeLogToSocket(clockClassOut)
		} else if e.clockClassMetric != nil {
			e.clockClassMetric.With(prometheus.Labels{
				"process": PTP4lProcessName, "config": clk.cfgName, "node": e.nodeName}).Set(float64(clockClass))
		}
		fmt.Printf("%s", clockClassOut)
	}
}

func getMetricName(valueType ValueType) string {
	if strings.HasSuffix(string(valueType), string(OFFSET)) {
		return fmt.Sprintf("%s_%s", valueType, "ns")
	}
	return string(valueType)
}

// SetPortRole saves the port role change event
func (e *EventHandler) SetPortRole(cfgName, portNane string, event *parser.PTPEvent) {
	e.Lock()
	defer e.Unlock()
	if e.portRole == nil {
		e.portRole = make(map[string]map[string]*parser.PTPEvent)
	}
	if _, ok := e.portRole[cfgName]; !ok {
		e.portRole[cfgName] = make(map[string]*parser.PTPEvent)
	}
	e.portRole[cfgName][portNane] = event
}

// EmitClockSyncLogs emits the clock sync state logs
func (e *EventHandler) EmitClockSyncLogs() {
	glog.Info("Re-emitting metrics logs for event-proxy as requested")

	if e.getConn() == nil {
		glog.Warning("Connection is nil, attempting to reconnect before emitting clock sync logs")
		if !e.reconnectEventSocket() {
			glog.Error("Failed to emit clock sync logs, reconnect failed")
			return
		}
	}
	// Snapshot clkSyncState logs under lock to avoid concurrent map access
	e.Lock()
	logs := make([]string, 0, len(e.clkSyncState))
	for _, syncState := range e.clkSyncState {
		if syncState.clkLog != "" {
			logs = append(logs, syncState.clkLog)
		}
	}
	e.Unlock()

	for _, l := range logs {
		glog.Info(l)
		if !e.writeLogToSocket(l) {
			glog.Warning("Broken pipe detected while emitting clock sync logs, stopping.")
			break
		}
	}
}

// EmitPortRoleLogs emits the port role logs
func (e *EventHandler) EmitPortRoleLogs() {
	if e.getConn() == nil {
		glog.Warning("Connection is nil, attempting to reconnect before emitting port role logs")
		if !e.reconnectEventSocket() {
			glog.Error("Failed to emit port state logs, reconnect failed")
			return
		}
	}
	glog.Info("Re-emitting metrics logs for event-proxy as requested")

	// Snapshot port role data under lock to avoid concurrent map access
	e.Lock()
	type portRoleEntry struct {
		raw string
	}
	var entries []portRoleEntry
	for _, ports := range e.portRole {
		for _, portEvent := range ports {
			if portEvent != nil {
				entries = append(entries, portRoleEntry{raw: portEvent.Raw})
			}
		}
	}
	e.Unlock()

	for _, entry := range entries {
		glog.Infof("Port Event %s", entry.raw)
		if !e.writeLogToSocket(entry.raw) {
			glog.Warning("Broken pipe detected while emitting port role logs, stopping.")
			break
		}
	}
}

// EmitProcessStatusLog writes a process status log entry to the event socket
// using the EventHandler's managed connection with reconnection support.
func (e *EventHandler) EmitProcessStatusLog(processName, cfgName string, status int64) {
	message := fmt.Sprintf("%s[%d]:[%s] PTP_PROCESS_STATUS:%d", processName, time.Now().Unix(), cfgName, status)
	glog.Info(message)
	e.writeLogToSocket(message)
}
