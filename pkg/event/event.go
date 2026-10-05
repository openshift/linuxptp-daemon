package event

import (
	"fmt"
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

// OffsetData is a ptp4l/ts2phc/phc2sys/chronyd offset sample.
type OffsetData struct {
	State      PTPState
	Offset     int64
	SourceLost bool
	NMEALocked *bool // ts2phc GNSS only
}

func (*OffsetData) eventData() {}

func (d *OffsetData) String() string {
	parts := make([]string, 0, 3)
	if d.NMEALocked != nil {
		parts = append(parts, fmt.Sprintf("%s %t", NMEA_STATUS, *d.NMEALocked))
	}
	parts = append(parts, fmt.Sprintf("%s %d", OFFSET, d.Offset), string(d.State))
	return strings.Join(parts, " ")
}

// DPLLData is a DPLL status sample. Offset/PhaseStatus/FrequencyStatus are nil when FlagNo* is set.
type DPLLData struct {
	State                    PTPState
	OutOfSpec                bool
	SourceLost               bool
	FrequencyTraceable       bool
	LeadingSource            bool
	Offset                   *int64
	PhaseStatus              *int64
	FrequencyStatus          *int64
	PPSStatus                int
	InSyncConditionThreshold uint64
	InSyncConditionTimes     uint64
	ToFreeRunThreshold       uint64
	MaxInSpecOffset          uint64
}

func (*DPLLData) eventData() {}

func (d *DPLLData) String() string {
	parts := make([]string, 0, 5)
	if d.FrequencyStatus != nil {
		parts = append(parts, fmt.Sprintf("%s %d", FREQUENCY_STATUS, *d.FrequencyStatus))
	}
	if d.Offset != nil {
		parts = append(parts, fmt.Sprintf("%s %d", OFFSET, *d.Offset))
	}
	if d.PhaseStatus != nil {
		parts = append(parts, fmt.Sprintf("%s %d", PHASE_STATUS, *d.PhaseStatus))
	}
	parts = append(parts, fmt.Sprintf("%s %d", PPS_STATUS, d.PPSStatus), string(d.State))
	return strings.Join(parts, " ")
}

// SyncEData is a synce4l quality or EEC state sample. QL/ExtQL are nil when that TLV is absent.
type SyncEData struct {
	State         PTPState
	Device        string
	QL            *byte
	ExtQL         *byte
	ClockQuality  string
	EECState      string
	NetworkOption int
}

func (*SyncEData) eventData() {}

func (d *SyncEData) String() string {
	parts := make([]string, 0, 7)
	if d.ClockQuality != "" {
		parts = append(parts, fmt.Sprintf("%s %s", CLOCK_QUALITY, d.ClockQuality))
	}
	if d.Device != "" {
		parts = append(parts, fmt.Sprintf("%s %s", DEVICE, d.Device))
	}
	if d.EECState != "" {
		parts = append(parts, fmt.Sprintf("%s %s", EEC_STATE, d.EECState))
	}
	if d.ExtQL != nil {
		parts = append(parts, fmt.Sprintf("%s %#x", EXT_QL, *d.ExtQL))
	}
	if d.NetworkOption != 0 {
		parts = append(parts, fmt.Sprintf("%s %d", NETWORK_OPTION, d.NetworkOption))
	}
	if d.QL != nil {
		parts = append(parts, fmt.Sprintf("%s %#x", QL, *d.QL))
	}
	if d.State != "" && d.State != PTP_UNKNOWN {
		parts = append(parts, string(d.State))
	}
	return strings.Join(parts, " ")
}

// StateData is a lock/holdover transition. ClockID and ControlledPortsConfig are set on T-BC transitions.
type StateData struct {
	State                 PTPState
	SourceLost            bool
	ControlledPortsConfig string
	ClockID               string
}

func (*StateData) eventData() {}

func (d *StateData) String() string {
	parts := make([]string, 0, 3)
	if d.ClockID != "" {
		parts = append(parts, fmt.Sprintf("%s %s", ClockIDKey, d.ClockID))
	}
	if d.ControlledPortsConfig != "" {
		parts = append(parts, fmt.Sprintf("%s %s", ControlledPortsConfig, d.ControlledPortsConfig))
	}
	parts = append(parts, string(d.State))
	return strings.Join(parts, " ")
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

// SelectedSourceData signals which source interface phc2sys has selected to
// discipline the system clock — the HA failover selection. It is emitted by the
// phc2sys processor from the "selecting X as [out-of-domain] source clock" log
// line and consumed by the HA clock to track its active member. IFace is the
// selected interface.
type SelectedSourceData struct {
	IFace string
}

func (*SelectedSourceData) eventData() {}

func (d *SelectedSourceData) String() string {
	return fmt.Sprintf("selected source %s", d.IFace)
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

// Ptr returns a pointer to the type you pass in it the type is ambiguious
// e.g. Ptr(15) you have coherse the type by using Ptr[int64](15)
func Ptr[T any](v T) *T { return &v }

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
	// HA is a High Availability clock: a composite of multiple BC instances with a
	// single phc2sys that fails over between them to discipline the system clock.
	HA ClockType = "HA"
	// SysClock only the system clock not a PTP Clock (this is updated by another clock)
	SysClock ClockType = "SysClock"
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

// PluginEvent creates an event emitted by a plugin.
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
