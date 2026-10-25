package parser

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"

// StatusMetric represents a status value with its type/subtype
type StatusMetric struct {
	Subtype string  `json:"subtype"` // Type of status (e.g., "frequency_status", "phase_status", "pps_status", "nmea_status")
	Status  float64 `json:"status"`  // Status value (e.g., 0, 1, 2, 3)
}

// PTPEvent represents an event extracted from a log line.
type PTPEvent struct {
	PortID     int                   `json:"portid"`
	Iface      string                `json:"iface"`
	Role       constants.PTPPortRole `json:"role"`       // e.g. SLAVE, MASTER, FAULTY
	ClockState constants.ClockState  `json:"clockstate"` // Clock class value for clock class change events
	Raw        string                `json:"raw"`        // original line
	Ts2Phc     *Ts2PhcEvent          `json:"ts2phc,omitempty"`
}

// TS2PHCRole describes a PHC's role in ts2phc synchronization.
// The empty value means that an event does not describe a role change.
type TS2PHCRole string

const (
	// TS2PHCSink identifies a PHC selected as a synchronization sink.
	TS2PHCSink TS2PHCRole = "sink"
	// TS2PHCSource identifies the synchronization source PHC.
	TS2PHCSource TS2PHCRole = "source"
	// TS2PHCUnmanaged identifies a PHC with no known synchronization role.
	TS2PHCUnmanaged TS2PHCRole = "unmanaged"
)

// PHCTimestamp is a hardware timestamp split into whole seconds and nanoseconds.
type PHCTimestamp struct {
	Seconds     int64 `json:"seconds"`
	Nanoseconds int64 `json:"nanoseconds"`
}

// Ts2PhcEvent carries a parsed ts2phc timestamp, poll timeout, selection change,
// or successful offset report. Poll tracking and PHC-to-interface resolution belong to the
// daemon, not the parser. For PollSuccessful, PTPEvent.Iface identifies the clock.
type Ts2PhcEvent struct {
	Timestamp      string        `json:"timestamp"`
	PHCDevice      string        `json:"phcDevice,omitempty"`
	PHCTstamp      *PHCTimestamp `json:"phcTstamp,omitempty"`
	PollTimeout    bool          `json:"pollTimeout"`
	PollSuccessful bool          `json:"pollSuccessful"`
	Role           TS2PHCRole    `json:"role,omitempty"`
}

// Note: metrics should be float64 values for as thatis the type expected by the prometheus client library.

// Metrics represents the metrics extracted from a log line.
type Metrics struct {
	From       string               `json:"from"`
	Iface      string               `json:"iface"` // Interface or CLOCK_REALTIME
	Offset     float64              `json:"offset"`
	MaxOffset  float64              `json:"maxoffset"`
	FreqAdj    float64              `json:"freqadj"`
	Delay      float64              `json:"delay"`
	ClockState constants.ClockState `json:"clockstate"` // e.g. LOCKED, FREERUN, HOLDOVER
	ServoState string               `json:"servostate"` // raw ptp4l servo state: "s0","s1","s2","s3"; unset on non-servo lines
	Source     string               `json:"source"`     // e.g. "phc", "sys", or "master"
	Status     []StatusMetric       `json:"status"`     // List of status metrics with their subtypes
}
