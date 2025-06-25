package parser

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"

// StatusMetric represents a status value with its type/subtype
type StatusMetric struct {
	Subtype string  // Type of status (e.g., "frequency_status", "phase_status", "pps_status", "nmea_status")
	Status  float64 // Status value (e.g., 0, 1, 2, 3)
}

// PTPEvent represents an event extracted from a log line.
type PTPEvent struct {
	PortID     int
	Iface      string
	Role       constants.PTPPortRole // e.g. SLAVE, MASTER, FAULTY
	ClockState constants.ClockState  // Clock class value for clock class change events
	Raw        string                // original line
}

// Note: metrics should be float64 values for as thatis the type expected by the prometheus client library.

// Metrics represents the metrics extracted from a log line.
type Metrics struct {
	From       string
	Iface      string // Interface or CLOCK_REALTIME
	Offset     float64
	MaxOffset  float64
	FreqAdj    float64
	Delay      float64
	ClockState constants.ClockState // e.g. LOCKED, FREERUN, HOLDOVER
	Source     string               // e.g. "phc", "sys", or "master"
	Status     []StatusMetric       // List of status metrics with their subtypes
}
