package event

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
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
	LeadingSource             ValueType = "LeadingSource"
	InSyncConditionThreshold  ValueType = "in-sync-th"
	InSyncConditionTimes      ValueType = "in-sync-times"
	ToFreeRunThreshold        ValueType = "free-run_th"
	ControlledPortsConfig     ValueType = "controlled-ports-config"
	ParentDataSetKey          ValueType = "parent-ds"
	CurrentDataSetKey         ValueType = "current-ds"
	ClockIDKey                ValueType = "clock-id"
	TimePropertiesDataSet     ValueType = "time-props"
	MaxInSpecOffset           ValueType = "max-in-spec"
)

// ValueTypeHelpTxt provides help text for PTP value types.
var ValueTypeHelpTxt = map[ValueType]string{
	OFFSET:           "0 = FREERUN, 1 = LOCKED, 2 = HOLDOVER",
	GPS_STATUS:       "0=NOFIX, 1=Dead Reckoning Only, 2=2D-FIX, 3=3D-FIX, 4=GPS+dead reckoning fix, 5=Time only fix",
	PHASE_STATUS:     "-1=UNKNOWN, 0=INVALID, 1=FREERUN, 2=LOCKED, 3=LOCKED_HO_ACQ, 4=HOLDOVER",
	FREQUENCY_STATUS: "-1=UNKNOWN, 0=INVALID, 1=FREERUN, 2=LOCKED, 3=LOCKED_HO_ACQ, 4=HOLDOVER",
	NMEA_STATUS:      "0 = UNAVAILABLE, 1 = AVAILABLE",
	PPS_STATUS:       "0 = UNAVAILABLE, 1 = AVAILABLE",
}

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
	CHRONYD    EventSource = "chronyd"
	MONITORING EventSource = "monitoring"
	PMC        EventSource = "pmc"
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

// Clock state metric values for openshift_ptp_clock_state gauge.
const (
	ClockStateFreerun  float64 = 0
	ClockStateLocked   float64 = 1
	ClockStateHoldover float64 = 2
)

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

// PtpClockThreshold carries the resolved clock thresholds used by BC/OC state
// decisions: the offset bounds (ns) that gate LOCKED/FREERUN and the holdover
// timeout (secs) used when the ptp4l source is lost.
type PtpClockThreshold struct {
	MaxOffsetThreshold int64
	MinOffsetThreshold int64
	HoldOverTimeout    int64
}

// HoldoverExpired signals that a BC/OC holdover timer has elapsed. The clock's
// own timer posts it back onto the event loop so the transition to FREERUN is
// decided on the loop (and only if the clock is still in HOLDOVER), never from
// the timer goroutine.
//
// Generation identifies which armed timer produced this event. The clock arms a
// new generation each time it (re)enters holdover, so an expiry queued by a
// timer that was since canceled or superseded (e.g. a re-lock followed by a
// fresh holdover) can be ignored instead of cutting the current holdover short.
type HoldoverExpired struct {
	Generation uint64
}

func (*HoldoverExpired) eventData() {}

// ParentTimeCurrentDS carries the upstream parent/time/current datasets fetched
// via PMC, tagged with the announce token that requested the fetch.
type ParentTimeCurrentDS struct {
	ParentTimeCurrentDS pmc.ParentTimeCurrentDS
	// Generation stamps the clock lifecycle epoch that requested this fetch, so
	// a result produced during a previous epoch (before a Reset or a clock
	// replacement) can be recognized as stale and dropped on the state loop.
	Generation uint64
}

func (*ParentTimeCurrentDS) eventData() {}

// ParentDSData carries a PARENT_DATA_SET update from the PMC poller.
type ParentDSData struct {
	ParentDataSet protocol.ParentDataSet
}

func (*ParentDSData) eventData() {}

// ClockType ...
type ClockType string

const (
	// GM ..
	GM ClockType = "GM"
	// BC ...
	BC ClockType = "BC"
	// TBC is a Telco Boundary Clock (T-BC) with DPLL, ts2phc, and E810 plugin.
	TBC ClockType = "T-BC"
	// OC ...
	OC ClockType = "OC"
	// ClockUnset ...
	ClockUnset ClockType = ""
)

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

// PtpStateToIPCState converts PTP state to IPC state string.
func PtpStateToIPCState(s PTPState) string {
	switch s {
	case PTP_LOCKED:
		return ipc.StateLocked
	case PTP_HOLDOVER:
		return ipc.StateHoldover
	default:
		return ipc.StateFreerun
	}
}
