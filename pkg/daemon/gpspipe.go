package daemon

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
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

type gpspipe struct {
	name       string
	execMutex  sync.Mutex
	cmdLine    string
	cmd        *exec.Cmd
	serialPort string
	exitCh     chan struct{}
	stopped    bool
	messageTag string
	c          *net.Conn
}

// Name ... Process name
func (gp *gpspipe) Name() string {
	return gp.name
}

// ExitCh ... exit channel
func (gp *gpspipe) ExitCh() chan struct{} {
	return gp.exitCh
}

// SerialPort ... get SerialPort
func (gp *gpspipe) SerialPort() string {
	return gp.serialPort
}
func (gp *gpspipe) setStopped(val bool) {
	gp.execMutex.Lock()
	gp.stopped = val
	gp.execMutex.Unlock()
}

// Stopped ... check if gpspipe is stopped
func (gp *gpspipe) Stopped() bool {
	gp.execMutex.Lock()
	me := gp.stopped
	gp.execMutex.Unlock()
	return me
}

// CmdStop ... stop gpspipe
func (gp *gpspipe) CmdStop() {
	defer func() {
		// Clean up (delete) the named pipe, this ensures that the named pipe is deleted when the process is terminated
		// no stale data is left in the named pipe
		err := os.Remove(GPSPIPE_SERIALPORT)
		if err != nil && !os.IsNotExist(err) {
			glog.Errorf("Failed to delete named pipe: %s", GPSPIPE_SERIALPORT)
		}
		glog.Infof("Process %s terminated", gp.name)
	}()
	glog.Infof("stopping %s...", gp.name)

	// Always set the stopped flag first
	gp.setStopped(true)
	gp.ProcessStatus(nil, PtpProcessDown)

	if gp.cmd == nil {
		return
	}

	if gp.cmd.Process != nil {
		glog.Infof("Sending TERM to (%s) PID: %d", gp.name, gp.cmd.Process.Pid)
		signalErr := gp.cmd.Process.Signal(syscall.SIGTERM)
		if signalErr != nil {
			glog.Errorf("Failed to send term signal to named pipe: %s", GPSPIPE_SERIALPORT)
			return
		}

		// Wait for process to terminate with timeout
		done := make(chan error, 1)
		go func() {
			done <- gp.cmd.Wait()
		}()

		select {
		case err := <-done:
			if err != nil {
				glog.Warningf("Process %s exited with error: %v", gp.name, err)
			} else {
				glog.Infof("Process %s terminated gracefully", gp.name)
			}
		case <-time.After(5 * time.Second):
			glog.Warningf("Process %s did not terminate gracefully, sending SIGKILL", gp.name)
			if err := gp.cmd.Process.Signal(syscall.SIGKILL); err != nil {
				glog.Errorf("Failed to send SIGKILL to process %s: %v", gp.name, err)
			}
			// Add a second timeout to ensure Wait() doesn't hang forever
			select {
			case err := <-done:
				glog.Warningf("Process %s exited after SIGKILL: %v", gp.name, err)
			case <-time.After(2 * time.Second):
				glog.Errorf("Process %s failed to exit even after SIGKILL", gp.name)
			}
		}

	}

}

// CmdInit ... initialize gpspipe
func (gp *gpspipe) CmdInit() {
	if gp.name == "" {
		gp.name = GPSPIPE_PROCESSNAME
	}
	gp.cmdLine = fmt.Sprintf("/usr/local/bin/gpspipe -v -R -l -o %s", gp.SerialPort())
}

func (gp *gpspipe) ProcessStatus(c *net.Conn, status int64) {
	if c != nil {
		gp.c = c
	}
	processStatus(gp.c, gp.name, gp.messageTag, status)
}

// CmdRun ... run gpspipe
func (gp *gpspipe) CmdRun(stdoutToSocket bool) {
	defer func() {
		select {
		case gp.exitCh <- struct{}{}:
		default:
		}
	}()

	for {
		// Check if we should stop before starting a new process
		if gp.Stopped() {
			gp.ProcessStatus(nil, PtpProcessDown)
			glog.Infof("Process %s terminated and will not be restarted. Exiting.", gp.name)
			break
		}

		// Ensure named pipe is created before starting the process
		// This handles cases where another process might have deleted the named pipe
		if err := mkFifo(); err != nil {
			glog.Errorf("Failed to create named pipe: %v", err)
			return // Exit the process, let the daemon restart it, since mkFifo is critical for GNSS monitoring
			// and it panics if it fails
		}

		gp.ProcessStatus(nil, PtpProcessUp)
		glog.Infof("Starting %s...", gp.Name())
		glog.Infof("%s cmd: %+v", gp.Name(), gp.cmd)
		gp.cmd.Stderr = os.Stderr

		// Start the process
		err := gp.cmd.Start()
		if err != nil {
			glog.Errorf("CmdRun() error starting %s: %v", gp.Name(), err)
			// Wait before retrying
			time.Sleep(1 * time.Second)
			continue
		}

		// Wait for the process to complete using goroutine pattern
		err = gp.cmd.Wait()
		if err != nil {
			glog.Errorf("CmdRun() error waiting for %s: %v, attempting to restart", gp.Name(), err)
		}

		// Check if we should stop after process completes
		if gp.Stopped() {
			gp.ProcessStatus(nil, PtpProcessDown)
			glog.Infof("Process %s terminated and will not be restarted. Exiting.", gp.name)
			break
		}

		// Create new command for restart
		gp.cmd = exec.Command(gp.cmd.Args[0], gp.cmd.Args[1:]...)
		time.Sleep(1 * time.Second)
	}
}

// mkFifo creates the named pipe if it doesn't exist, or removes and recreates it if it does
// Retries up to 5 times with exponential backoff if the operation fails
// Panics if all attempts fail as this is critical for GNSS monitoring (unless in test environment)
// Uses global mutex to prevent race conditions when multiple gpspipe instances are created simultaneously
func mkFifo() error {
	gpspipeGlobalMutex.Lock()
	defer gpspipeGlobalMutex.Unlock()

	const maxRetries = 5
	const baseDelay = 100 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := createNamedPipe()
		if err == nil {
			if attempt == 1 {
				glog.Infof("Successfully created named pipe: %s", GPSPIPE_SERIALPORT)
			} else {
				glog.Infof("Successfully created named pipe: %s (after %d attempts)", GPSPIPE_SERIALPORT, attempt)
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
func createNamedPipe() error {
	// Step 1: Ensure the directory exists
	if err := ensureDirectoryExists(); err != nil {
		return err
	}

	// Step 2: Remove existing named pipe if it exists
	if err := removeExistingPipe(); err != nil {
		return err
	}

	// Step 3: Create the new named pipe
	if err := createNewPipe(); err != nil {
		return err
	}

	return nil
}

// ensureDirectoryExists creates the GPSD directory if it doesn't exist
// If the directory exists but has issues, it will be removed and recreated
func ensureDirectoryExists() error {
	// Check if directory already exists
	if _, err := os.Stat(GPSD_DIR); err == nil {
		// Directory exists, check if it's valid
		if isValidDirectory(GPSD_DIR) {
			// Directory is valid, no need to recreate
			return nil
		}

		// Directory exists but is invalid, remove it
		glog.Infof("Directory %s exists but is invalid, removing and recreating", GPSD_DIR)
		if err = os.RemoveAll(GPSD_DIR); err != nil {
			return fmt.Errorf("failed to remove invalid directory %s: %v", GPSD_DIR, err)
		}
	} else if !os.IsNotExist(err) {
		// Some other error occurred (not just "doesn't exist")
		return fmt.Errorf("failed to check directory %s: %v", GPSD_DIR, err)
	}

	// Create the directory (either it didn't exist or we just removed it)
	if err := os.MkdirAll(GPSD_DIR, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create directory %s: %v", GPSD_DIR, err)
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
func removeExistingPipe() error {
	// Check if named pipe exists
	if _, err := os.Stat(GPSPIPE_SERIALPORT); err == nil {
		// Named pipe exists, remove it
		glog.Infof("Named pipe %s already exists, removing it", GPSPIPE_SERIALPORT)
		err = os.Remove(GPSPIPE_SERIALPORT)
		if err != nil {
			return fmt.Errorf("failed to remove existing named pipe %s: %v", GPSPIPE_SERIALPORT, err)
		}
	} else if !os.IsNotExist(err) {
		// Some other error occurred (not just "doesn't exist")
		return fmt.Errorf("failed to check named pipe %s: %v", GPSPIPE_SERIALPORT, err)
	}
	// If os.IsNotExist(err) is true, the pipe doesn't exist, which is fine
	return nil
}

// createNewPipe creates the named pipe using syscall.Mkfifo
func createNewPipe() error {
	if err := syscall.Mkfifo(GPSPIPE_SERIALPORT, 0600); err != nil {
		return fmt.Errorf("failed to create named pipe %s: %v", GPSPIPE_SERIALPORT, err)
	}
	return nil
}

// MonitorProcess ... monitor gpspipe
func (gp *gpspipe) MonitorProcess(config config.ProcessConfig) {
	//TODO implement me
	glog.Infof("monitoring for gpspipe not implemented")
}
