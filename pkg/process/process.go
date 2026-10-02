package process

import (
	"context"
	"fmt"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// Action is an enumeration for process control operations.
type Action int

// Action enumeration for process control operations.
const (
	ActionStart   Action = iota // launch the process
	ActionStop                  // gracefully stop the process
	ActionRestart               // stop then start
	ActionEnable
	ActionDisable
)

// State is an enumeration for lifecycle stages of a process.
type State int

// process.State enumeration for lifecycle stages of a process.
const (
	// Created but no actions have been applied
	Created State = iota
	// Process amanger has requested a start but the process is not yet running.
	Starting
	// Process and all go routines are running and no further actions have been requested
	Running
	// Process manager has requested stop but process or other goroutines have not exited yet
	Stopping
	// Stopped by process manager
	Stopped
	// Process exited early
	Dead
)

// String returns the string representation of the State.
func (s State) String() string {
	switch s {
	case Created:
		return "Created"
	case Starting:
		return "Starting"
	case Running:
		return "Running"
	case Stopping:
		return "Stopping"
	case Stopped:
		return "Stopped"
	case Dead:
		return "Dead"
	default:
		return fmt.Sprintf("ProcessState(%d)", int(s))
	}
}

// Process represents a manageable PTP process with lifecycle control and event-driven conditions.
type Process interface {
	Name() string
	ConfigName() string
	Start(ctx context.Context) error
	Stop() error
	Conditions() map[Action]Condition
	State() State
	Profile() *ptpv1.PtpProfile    // Should go away
	ClockType() event.ClockType    // Should go away
	DependentProcesses() []Process // Should go away
	SyncInitialState()
}
