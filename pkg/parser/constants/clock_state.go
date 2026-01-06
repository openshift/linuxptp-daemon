// Package constants provides constants used throughout the parser package.
package constants

// ClockState ...
type ClockState string

const (
	//ClockStateLocked ...
	ClockStateLocked ClockState = "LOCKED"
	//ClockStateFreeRun ...
	ClockStateFreeRun = "FREERUN"
	// ClockStateHoldover ...
	ClockStateHoldover = "HOLDOVER"
)
