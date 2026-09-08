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
	GPS_STATUS ValueType = "gnss_status"
	//Status           ValueType = "status"
	PHASE_STATUS              ValueType = "phase_status"
	FREQUENCY_STATUS          ValueType = "frequency_status"
	NMEA_STATUS               ValueType = parserconstants.NmeaStatus
	PROCESS_STATUS            ValueType = "process_status" // 0 = DOWN, 1 = UP (PtpProcessDown / PtpProcessUp)
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
	ClockIDKey                ValueType = "clock-id"
	MaxInSpecOffset           ValueType = "max-in-spec"
)

const (
	// ProcessStatusUnset is stored on Data before any up/down event. 0 is down, 1 is up.
	ProcessStatusUnset int64 = -1
)

// ValueTypeHelpTxt provides help text for PTP value types.
var ValueTypeHelpTxt = map[ValueType]string{
	OFFSET:           "0 = FREERUN, 1 = LOCKED, 2 = HOLDOVER",
	GPS_STATUS:       "0=NOFIX, 1=Dead Reckoning Only, 2=2D-FIX, 3=3D-FIX, 4=GPS+dead reckoning fix, 5=Time only fix",
	PHASE_STATUS:     "-1=UNKNOWN, 0=INVALID, 1=FREERUN, 2=LOCKED, 3=LOCKED_HO_ACQ, 4=HOLDOVER",
	FREQUENCY_STATUS: "-1=UNKNOWN, 0=INVALID, 1=FREERUN, 2=LOCKED, 3=LOCKED_HO_ACQ, 4=HOLDOVER",
	NMEA_STATUS:      "0 = UNAVAILABLE, 1 = AVAILABLE",
	PPS_STATUS:       "0 = UNAVAILABLE, 1 = AVAILABLE",
	PROCESS_STATUS:   "0 = DOWN, 1 = UP",
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
	GPSD       EventSource = "gpsd"
	GPSPIPE    EventSource = "gpspipe"
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
//
//nolint:revive // "Data" conflicts with stats.go Data struct
type EventData interface {
	eventData()
	String() string
}

// GNSSData carries GNSS receiver status. It does not use PTPState.
type GNSSData struct {
	GPSStatus  int64
	Offset     int64
	SourceLost bool // GPS fix lost (status < 3) or offset out of range
}

func (*GNSSData) eventData() {}

func (d *GNSSData) String() string {
	state := PTP_FREERUN
	if d.GPSStatus >= 3 && !d.SourceLost {
		state = PTP_LOCKED
	}
	return fmt.Sprintf("%s %d %s %d %s", GPS_STATUS, d.GPSStatus, OFFSET, d.Offset, state)
}

// PTPData carries PTP synchronization status (DPLL, ts2phc, ptp4l, SyncE).
type PTPData struct {
	State              PTPState
	Values             map[ValueType]interface{}
	OutOfSpec          bool
	SourceLost         bool
	FrequencyTraceable bool
}

func (*PTPData) eventData() {}

func (d *PTPData) String() string {
	logData := make([]string, 0, len(d.Values))
	for k, v := range d.Values {
		switch val := v.(type) {
		case int64:
			logData = append(logData, fmt.Sprintf("%s %d", k, val))
		case int:
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
	if d.State != "" && d.State != PTP_UNKNOWN {
		logData = append(logData, string(d.State))
	}
	return strings.Join(logData, " ")
}

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

func (he *HoldoverExpired) String() string {
	return fmt.Sprintf("Holover Expired: generation=%d", he.Generation)
}

// ProcessStatusData is process up/down. Status is 0 (down) or 1 (up).
type ProcessStatusData struct {
	Status int64
}

func (*ProcessStatusData) eventData() {}

func (d *ProcessStatusData) String() string {
	return fmt.Sprintf("PTP_PROCESS_STATUS:%d", d.Status)
}

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

func (d *ParentTimeCurrentDS) String() string { return "" }

// ParentDSData carries a PARENT_DATA_SET update from the PMC poller.
type ParentDSData struct {
	ParentDataSet protocol.ParentDataSet
}

func (*ParentDSData) eventData() {}

func (d *ParentDSData) String() string { return "" }

// PluginData carries a plugin-emitted event (e.g. ntpfailover FSM transitions).
type PluginData struct {
	EventName string
}

func (*PluginData) eventData() {}

func (d *PluginData) String() string { return d.EventName }

// Int64Ptr returns a pointer to v. Used by DPLL/offset senders for optional fields.
func Int64Ptr(v int64) *int64 { return &v }

// BytePtr returns a pointer to v. Used by SyncE senders for optional QL fields.
func BytePtr(v byte) *byte { return &v }

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
	Data       EventData // typed payload, or nil for reset events
}

// ProcessStatusEvent is a process Up/Down event. status is 0 (down) or 1 (up).
func ProcessStatusEvent(source EventSource, cfgName string, clockType ClockType, iface string, status int64) Event {
	return Event{
		Source:     source,
		IFace:      iface,
		CfgName:    cfgName,
		ClockType:  clockType,
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data:       &ProcessStatusData{Status: status},
	}
}

// PluginEvent creates an event emitted by a plugin (e.g. "gnss_failover", "gnss_recovered").
func PluginEvent(source EventSource, eventName string) Event {
	return Event{
		Source: source,
		Time:   time.Now().UnixMilli(),
		Data:   &PluginData{EventName: eventName},
	}
}

// GetLogData returns a formatted log line for the event.
func (e *Event) GetLogData() string {
	prefix := fmt.Sprintf("%s[%d]:[%s]", e.Source, time.Now().Unix(), e.CfgName)
	parts := []string{prefix}
	if e.IFace != "" {
		parts = append(parts, e.IFace)
	}
	if e.Data != nil {
		if s := e.Data.String(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ") + "\n"
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
