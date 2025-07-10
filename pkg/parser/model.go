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
	Source     string               `json:"source"`     // e.g. "phc", "sys", or "master"
	Status     []StatusMetric       `json:"status"`     // List of status metrics with their subtypes
}
