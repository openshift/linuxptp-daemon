package parser

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"

// PTPEvent represents an event extracted from a log line.
type PTPEvent struct {
	PortID int
	Iface  string
	Role   constants.PTPPortRole // e.g. SLAVE, MASTER, FAULTY
	Raw    string
	// original line
}

// Metrics represents the metrics extracted from a log line.
type Metrics struct {
	ConfigName string
	From       string
	Iface      string // Interface or CLOCK_REALTIME
	Offset     float64
	MaxOffset  float64
	FreqAdj    float64
	Delay      float64
	ClockState constants.ClockState // e.g. LOCKED, FREERUN, HOLDOVER
	Source     string               // e.g. "phc", "sys", or "master"
}
