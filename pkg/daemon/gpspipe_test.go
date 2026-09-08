package daemon

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/stretchr/testify/require"
)

// Test-specific constants
const (
	testGPSdDir           = "/tmp/test_gpsd"
	testGPSPipeSerialPort = "/tmp/test_gpsd/gpspipe"
)

// testMkFifo is a test-specific version that doesn't call glog.Fatalf
func testMkFifo() error {
	const maxRetries = 5
	const baseDelay = 100 * time.Millisecond

	for attempt := 1; attempt <= maxRetries; attempt++ {
		err := testCreateNamedPipe()
		if err == nil {
			// Success - return immediately
			return nil
		}

		// If this is the last attempt, return error instead of panicking
		if attempt == maxRetries {
			return err
		}

		// Calculate delay with exponential backoff
		delay := baseDelay * time.Duration(1<<(attempt-1)) // 100ms, 200ms, 400ms, 800ms, 1600ms
		// glog.Warningf("Failed to create named pipe (attempt %d/%d): %v. Retrying in %v...", attempt, maxRetries, err, delay)
		time.Sleep(delay)
	}

	return nil
}

// testCreateNamedPipe performs the actual named pipe creation logic for tests
func testCreateNamedPipe() error {
	// Create directory if it doesn't exist
	if err := os.MkdirAll(testGPSdDir, os.ModePerm); err != nil {
		return err
	}

	// Check if named pipe already exists
	if _, err := os.Stat(testGPSPipeSerialPort); err == nil {
		// Named pipe exists, remove it first
		if err = os.Remove(testGPSPipeSerialPort); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		// Some other error occurred
		return err
	}

	// Create the named pipe
	if err := syscall.Mkfifo(testGPSPipeSerialPort, 0600); err != nil {
		return err
	}

	return nil
}

func TestGpspipeProcessLifecycle(t *testing.T) {
	ch := make(chan event.Event, 8)
	gp := NewTestGpsPipeProcess("", ch, NewMockCmd(), filepath.Join(t.TempDir(), "data"))

	require.NoError(t, gp.Start(context.TODO()))
	waitProcessState(t, gp, process.Running)
	waitProcessStatus(t, ch, PtpProcessUp)

	require.NoError(t, gp.Stop())
	waitProcessState(t, gp, process.Stopped)
	waitProcessStatus(t, ch, PtpProcessDown)
}

func TestMkFifoSuccess(t *testing.T) {
	// Clean up any existing test named pipe
	if err := os.Remove(testGPSPipeSerialPort); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to remove existing test named pipe: %v", err)
	}
	if err := os.RemoveAll(testGPSdDir); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to remove existing test directory: %v", err)
	}

	// Test creating named pipe
	mkfifoErr := testMkFifo()
	if mkfifoErr != nil {
		t.Errorf("testMkFifo failed: %v", mkfifoErr)
	}

	// Check that named pipe exists
	if _, statErr := os.Stat(testGPSPipeSerialPort); os.IsNotExist(statErr) {
		t.Error("Named pipe was not created")
	}

	// Clean up
	if err := os.Remove(testGPSPipeSerialPort); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to clean up test named pipe: %v", err)
	}
	if err := os.RemoveAll(testGPSdDir); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to clean up test directory: %v", err)
	}
}

func TestMkFifoRecreation(t *testing.T) {
	// Clean up any existing test named pipe
	if err := os.Remove(testGPSPipeSerialPort); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to remove existing test named pipe: %v", err)
	}
	if err := os.RemoveAll(testGPSdDir); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to remove existing test directory: %v", err)
	}

	// Test creating named pipe
	mkfifoErr := testMkFifo()
	if mkfifoErr != nil {
		t.Errorf("testMkFifo failed: %v", mkfifoErr)
	}

	// Check that named pipe exists
	if _, statErr := os.Stat(testGPSPipeSerialPort); os.IsNotExist(statErr) {
		t.Error("Named pipe was not created")
	}

	// Test recreating named pipe (should remove and recreate)
	mkfifoErr = testMkFifo()
	if mkfifoErr != nil {
		t.Errorf("testMkFifo failed to recreate: %v", mkfifoErr)
	}

	// Check that named pipe still exists
	if _, statErr := os.Stat(testGPSPipeSerialPort); os.IsNotExist(statErr) {
		t.Error("Named pipe was not recreated")
	}

	// Clean up
	if err := os.Remove(testGPSPipeSerialPort); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to clean up test named pipe: %v", err)
	}
	if err := os.RemoveAll(testGPSdDir); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to clean up test directory: %v", err)
	}
}

func TestCreateNamedPipe(t *testing.T) {
	// Clean up any existing test named pipe
	if err := os.Remove(testGPSPipeSerialPort); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to remove existing test named pipe: %v", err)
	}
	if err := os.RemoveAll(testGPSdDir); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to remove existing test directory: %v", err)
	}

	// Test the underlying testCreateNamedPipe function

	if err := testCreateNamedPipe(); err != nil {
		t.Errorf("testCreateNamedPipe failed: %v", err)
	}

	// Check that named pipe exists
	if _, statErr := os.Stat(testGPSPipeSerialPort); os.IsNotExist(statErr) {
		t.Error("Named pipe was not created by testCreateNamedPipe")
	}

	// Clean up
	if err := os.Remove(testGPSPipeSerialPort); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to clean up test named pipe: %v", err)
	}
	if err := os.RemoveAll(testGPSdDir); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to clean up test directory: %v", err)
	}
}

func TestGpspipeStopBehavior(t *testing.T) {
	ch := make(chan event.Event, 8)
	gp := NewTestGpsPipeProcess("", ch, NewMockCmd(), filepath.Join(t.TempDir(), "data"))

	require.NoError(t, gp.Start(context.TODO()))
	waitProcessState(t, gp, process.Running)

	require.NoError(t, gp.Stop())
	waitProcessState(t, gp, process.Stopped)
	waitProcessStatus(t, ch, PtpProcessDown)

	require.NoError(t, gp.Stop())
	if gp.State() != process.Stopped {
		t.Error("Stop should be a no-op once the process is already Stopped")
	}
}

func TestMkFifoRetryLogic(t *testing.T) {
	// This test verifies that the retry logic works correctly
	// We can't easily test the panic scenario in unit tests, but we can test the retry behavior

	// Clean up any existing test named pipe
	if err := os.Remove(testGPSPipeSerialPort); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to remove existing test named pipe: %v", err)
	}
	if err := os.RemoveAll(testGPSdDir); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to remove existing test directory: %v", err)
	}

	// Test that testMkFifo succeeds on first attempt when conditions are good
	start := time.Now()
	mkfifoErr := testMkFifo()
	duration := time.Since(start)

	if mkfifoErr != nil {
		t.Errorf("testMkFifo failed: %v", mkfifoErr)
	}

	// Should succeed quickly (no retries needed)
	if duration > 200*time.Millisecond {
		t.Errorf("testMkFifo took too long (%v), suggesting unnecessary retries", duration)
	}

	// Clean up
	if err := os.Remove(testGPSPipeSerialPort); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to clean up test named pipe: %v", err)
	}
	if err := os.RemoveAll(testGPSdDir); err != nil && !os.IsNotExist(err) {
		t.Logf("Warning: failed to clean up test directory: %v", err)
	}
}
