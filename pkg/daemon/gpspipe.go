package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

const (
	// GPSPIPE_PROCESSNAME ... gpspipe process name
	GPSPIPE_PROCESSNAME = "gpspipe"
	// GPSPIPE_SERIALPORT ... gpspipe serial port
	GPSPIPE_SERIALPORT = "/gpsd/data"
	// GPSD_DIR ... gpsd directory
	GPSD_DIR = "/gpsd"
)

// Global mutex to protect shared directory and named pipe operations
// This prevents race conditions when multiple gpspipe instances are created simultaneously
var gpspipeGlobalMutex sync.Mutex

// GpsPipe is a process that pipes GPS data from serial port.
type GpsPipe struct {
	name       string
	execMutex  sync.RWMutex
	cmdLine    string
	cmd        ProcessCmd
	serialPort string
	stopCh     chan struct{}
	stopped    bool
	messageTag string
	eventCh    chan event.Event
	state      process.State
	profile    *ptpv1.PtpProfile
	conditions map[process.Action]process.Condition
	wg         sync.WaitGroup
}

// NewGpsPipeProcess creates a new GpsPipe process instance.
func NewGpsPipeProcess(messageTag string, nodeProfile *ptpv1.PtpProfile, eventCh chan event.Event) process.Process {
	cmdLine := addScheduling(nodeProfile, fmt.Sprintf("/usr/local/bin/gpspipe -v -R -l -o %s", GPSPIPE_SERIALPORT))
	args := strings.Split(cmdLine, " ")

	gp := &GpsPipe{
		name:       GPSPIPE_PROCESSNAME,
		cmdLine:    cmdLine,
		cmd:        NewExecCmd(exec.Command(args[0], args[1:]...)),
		serialPort: GPSPIPE_SERIALPORT,
		messageTag: messageTag,
		profile:    nodeProfile,
		eventCh:    eventCh,
		conditions: map[process.Action]process.Condition{},
	}
	return gp
}

// Name ... Process name
func (gp *GpsPipe) Name() string {
	return gp.name
}

// ConfigName ...
func (gp *GpsPipe) ConfigName() string {
	return cfgNameFromMessageTag(gp.messageTag)
}

// Conditions returns the action conditions for the GpsPipe process.
func (gp *GpsPipe) Conditions() map[process.Action]process.Condition {
	return gp.conditions
}

// SyncInitialState ...
func (gp *GpsPipe) SyncInitialState() {}

// State returns the current process state of GpsPipe.
func (gp *GpsPipe) State() process.State {
	gp.execMutex.RLock()
	defer gp.execMutex.RUnlock()
	return gp.state
}

func (gp *GpsPipe) setState(s process.State) {
	gp.execMutex.Lock()
	gp.state = s
	gp.execMutex.Unlock()
}

// Profile returns the PTP profile associated with GpsPipe.
func (gp *GpsPipe) Profile() *ptpv1.PtpProfile {
	return gp.profile
}

// ClockType returns the clock type for GpsPipe (always GM).
func (gp *GpsPipe) ClockType() event.ClockType {
	return event.GM
}

// DependentProcesses returns processes that depend on GpsPipe.
func (gp *GpsPipe) DependentProcesses() []process.Process {
	return []process.Process{}
}

// CmdStop ... stop gpspipe
func (gp *GpsPipe) Stop() error {
	defer func() {
		pipePath := gp.serialPort
		if pipePath == "" {
			pipePath = GPSPIPE_SERIALPORT
		}
		err := os.Remove(pipePath)
		if err != nil && !os.IsNotExist(err) {
			glog.Errorf("Failed to delete named pipe: %s", pipePath)
		}
		glog.Infof("Process %s terminated", gp.name)
	}()

	glog.Infof("stopping %s...", gp.name)
	gp.execMutex.Lock()
	st := gp.state
	if st == process.Created || st == process.Stopping || st == process.Stopped || st == process.Dead {
		gp.execMutex.Unlock()
		glog.Infof("%s is already stopped", gp.name)
		return nil
	}
	gp.state = process.Stopping
	cmd := gp.cmd
	stopCh := gp.stopCh
	gp.execMutex.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if cmd != nil {
		glog.Infof("Sending TERM to (%s) PID: %d", gp.name, cmd.Pid())
		signalErr := cmd.Signal(syscall.SIGTERM)
		if signalErr != nil {
			err := fmt.Errorf("failed to send term signal to named pipe: %s", GPSPIPE_SERIALPORT)
			glog.Error(err)
			return err
		}
	}
	gp.wg.Wait()
	return nil
}

// Start starts the GpsPipe process.
func (gp *GpsPipe) Start(_ context.Context) error {
	gp.execMutex.Lock()
	st := gp.state
	switch st {
	case process.Starting, process.Running:
		gp.execMutex.Unlock()
		glog.Infof("%s is already running", gp.name)
		return nil
	case process.Stopping:
		gp.execMutex.Unlock()
		return fmt.Errorf("%s is stopping", gp.name)
	}
	gp.stopCh = make(chan struct{})
	if gp.cmd != nil {
		gp.cmd = gp.cmd.Clone()
	}
	gp.state = process.Starting
	gp.execMutex.Unlock()

	gp.run()
	return nil
}

// ProcessStatus updates the process status for GpsPipe.
func (gp *GpsPipe) ProcessStatus(status int64) {
	processStatus(gp.name, gp.messageTag, status)
}

func (gp *GpsPipe) run() {
	gp.wg.Add(1)
	go func() {
		defer gp.wg.Done()
		defer func() {
			if gp.State() == process.Stopping {
				gp.setState(process.Stopped)
				glog.Infof("Process %s terminated", gp.name)
			} else {
				gp.setState(process.Dead)
				glog.Infof("Process %s exited unexpectedly", gp.name)
			}
			gp.ProcessStatus(PtpProcessDown)
			sendProcessStatusEvent(gp.eventCh, event.GPSPIPE, cfgNameFromMessageTag(gp.messageTag), gp.ClockType(), "", PtpProcessDown)
		}()

		gp.execMutex.RLock()
		cmd := gp.cmd
		pipePath := gp.serialPort
		gp.execMutex.RUnlock()
		if cmd == nil {
			glog.Errorf("run() nil cmd for %s", gp.Name())
			return
		}
		if pipePath == "" {
			pipePath = GPSPIPE_SERIALPORT
		}

		// Ensure named pipe is created before starting the process
		// This handles cases where another process might have deleted the named pipe
		if err := mkFifo(pipePath); err != nil {
			glog.Errorf("Failed to create named pipe: %v", err)
			return // Exit the process, let the daemon restart it, since mkFifo is critical for GNSS monitoring
			// and it panics if it fails
		}

		glog.Infof("Starting %s...", gp.Name())
		glog.Infof("%s cmd: %+v", gp.Name(), cmd)
		cmd.SetStderr(os.Stderr)

		err := cmd.Start()
		if err != nil {
			glog.Errorf("CmdRun() error starting %s: %v", gp.Name(), err)
			return
		}
		gp.execMutex.Lock()
		if gp.state == process.Stopping {
			gp.execMutex.Unlock()
			_ = cmd.Signal(syscall.SIGTERM)
		} else {
			gp.state = process.Running
			gp.execMutex.Unlock()
			gp.ProcessStatus(PtpProcessUp)
			sendProcessStatusEvent(gp.eventCh, event.GPSPIPE, cfgNameFromMessageTag(gp.messageTag), gp.ClockType(), "", PtpProcessUp)
		}

		err = cmd.Wait()
		if err != nil {
			glog.Errorf("CmdRun() error waiting for %s: %v, attempting to restart", gp.Name(), err)
		}
	}()
}

// mkFifo creates the named pipe if it doesn't exist, or removes and recreates it if it does
// Retries up to 5 times with exponential backoff if the operation fails
// Panics if all attempts fail as this is critical for GNSS monitoring (unless in test environment)
// Uses global mutex to prevent race conditions when multiple gpspipe instances are created simultaneously
func mkFifo(pipePath string) error {
	gpspipeGlobalMutex.Lock()
	defer gpspipeGlobalMutex.Unlock()

	const maxRetries = 5
	const baseDelay = 100 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := createNamedPipe(pipePath)
		if err == nil {
			if attempt == 1 {
				glog.Infof("Successfully created named pipe: %s", pipePath)
			} else {
				glog.Infof("Successfully created named pipe: %s (after %d attempts)", pipePath, attempt)
			}
			return nil
		}

		if attempt == maxRetries {
			msg := fmt.Sprintf("Failed to create named pipe after %d attempts: %v", maxRetries, err)
			if os.Getenv("SKIP_GNSS_MONITORING") == "1" {
				glog.Warning("Test environment: " + msg)
				return fmt.Errorf("failed to create named pipe after %d attempts", maxRetries)
			}
			glog.Fatalf("CRITICAL: %s. GNSS monitoring cannot continue.", msg)
		}

		// Exponential backoff: 100ms, 200ms, 400ms, etc.
		delay := baseDelay * time.Duration(1<<(attempt-1))
		glog.Warningf("Failed to create named pipe (attempt %d/%d): %v. Retrying in %v...", attempt, maxRetries, err, delay)
		time.Sleep(delay)
	}

	return nil // Unreachable, but required by compiler
}

// createNamedPipe performs the actual named pipe creation logic
func createNamedPipe(pipePath string) error {
	dir := filepath.Dir(pipePath)
	// Step 1: Ensure the directory exists
	if err := ensureDirectoryExists(dir); err != nil {
		return err
	}

	// Step 2: Remove existing named pipe if it exists
	if err := removeExistingPipe(pipePath); err != nil {
		return err
	}

	// Step 3: Create the new named pipe
	if err := createNewPipe(pipePath); err != nil {
		return err
	}

	return nil
}

// ensureDirectoryExists creates the GPSD directory if it doesn't exist
// If the directory exists but has issues, it will be removed and recreated
func ensureDirectoryExists(dir string) error {
	// Check if directory already exists
	if _, err := os.Stat(dir); err == nil {
		// Directory exists, check if it's valid
		if isValidDirectory(dir) {
			// Directory is valid, no need to recreate
			return nil
		}

		// Directory exists but is invalid, remove it
		glog.Infof("Directory %s exists but is invalid, removing and recreating", dir)
		if err = os.RemoveAll(dir); err != nil {
			return fmt.Errorf("failed to remove invalid directory %s: %v", dir, err)
		}
	} else if !os.IsNotExist(err) {
		// Some other error occurred (not just "doesn't exist")
		return fmt.Errorf("failed to check directory %s: %v", dir, err)
	}

	// Create the directory (either it didn't exist or we just removed it)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create directory %s: %v", dir, err)
	}

	return nil
}

// isValidDirectory checks if the directory is valid and usable
func isValidDirectory(dirPath string) bool {
	info, err := os.Stat(dirPath)
	if err != nil || !info.IsDir() {
		return false
	}
	testFile := filepath.Join(dirPath, ".test_write_access")
	if err = os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		return false
	}
	_ = os.Remove(testFile)
	return true
}

// removeExistingPipe removes the named pipe if it already exists
func removeExistingPipe(pipePath string) error {
	// Check if named pipe exists
	if _, err := os.Stat(pipePath); err == nil {
		// Named pipe exists, remove it
		glog.Infof("Named pipe %s already exists, removing it", pipePath)
		err = os.Remove(pipePath)
		if err != nil {
			return fmt.Errorf("failed to remove existing named pipe %s: %v", pipePath, err)
		}
	} else if !os.IsNotExist(err) {
		// Some other error occurred (not just "doesn't exist")
		return fmt.Errorf("failed to check named pipe %s: %v", pipePath, err)
	}
	// If os.IsNotExist(err) is true, the pipe doesn't exist, which is fine
	return nil
}

// createNewPipe creates the named pipe using syscall.Mkfifo
func createNewPipe(pipePath string) error {
	if err := syscall.Mkfifo(pipePath, 0600); err != nil {
		return fmt.Errorf("failed to create named pipe %s: %v", pipePath, err)
	}
	return nil
}
