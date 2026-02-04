package event

import (
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// newTestEventHandler creates a minimal EventHandler for testing socket logic.
func newTestEventHandler(socketPath string) *EventHandler {
	return &EventHandler{
		stdoutSocket:   socketPath,
		stdoutToSocket: true,
		closeCh:        make(chan bool, 1),
		brokenPipeCh:   make(chan struct{}, 1),
		clkSyncState:   map[string]*clockSyncState{},
	}
}

// shortSocketPath returns a short socket path that fits within Unix socket
// path length limits (104 bytes on macOS, 108 on Linux).
func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sock")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "t.sock")
}

// acceptAndClose accepts connections in a loop and closes them immediately.
// Useful for tests that only need to verify a connection can be established.
func acceptAndClose(listener net.Listener) {
	for {
		c, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		c.Close()
	}
}

// acceptAndHold accepts connections in a loop and keeps them open (discards ref).
func acceptAndHold(listener net.Listener) {
	for {
		_, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
	}
}

// acceptAndRead accepts connections and forwards received data to the channel.
func acceptAndRead(listener net.Listener, received chan<- string) {
	for {
		c, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 1024)
			for {
				n, readErr := c.Read(buf)
				if readErr != nil {
					return
				}
				received <- string(buf[:n])
			}
		}(c)
	}
}

// acceptAndReadOnce accepts one connection and reads a single message.
func acceptAndReadOnce(listener net.Listener, received chan<- string) {
	for {
		c, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 1024)
			n, _ := c.Read(buf)
			received <- string(buf[:n])
		}(c)
	}
}

// --- setConn / getConn ---

func TestGetConn_ReturnsNilWhenUnset(t *testing.T) {
	e := newTestEventHandler("")
	assert.Nil(t, e.getConn())
}

func TestSetConn_StoresConnection(t *testing.T) {
	e := newTestEventHandler("")
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	e.setConn(client)
	assert.Equal(t, client, e.getConn())
}

func TestSetConn_ClosesOldConnection(t *testing.T) {
	e := newTestEventHandler("")

	_, oldClient := net.Pipe()
	_, newClient := net.Pipe()
	defer newClient.Close()

	e.setConn(oldClient)
	e.setConn(newClient)

	// The old connection should be closed; writing to it should fail.
	_, err := oldClient.Write([]byte("test"))
	assert.Error(t, err, "old connection should be closed after setConn replaces it")
	assert.Equal(t, newClient, e.getConn())
}

func TestSetConn_NilClearsAndClosesOldConnection(t *testing.T) {
	e := newTestEventHandler("")

	_, client := net.Pipe()
	e.setConn(client)
	e.setConn(nil)

	assert.Nil(t, e.getConn())
	_, err := client.Write([]byte("test"))
	assert.Error(t, err, "connection should be closed after setConn(nil)")
}

func TestSetConn_SameConnectionNoClose(t *testing.T) {
	e := newTestEventHandler("")
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	e.setConn(client)
	e.setConn(client) // same connection, should not close it

	// Connection should still be usable.
	go func() {
		buf := make([]byte, 4)
		_, _ = server.Read(buf)
	}()
	_, err := client.Write([]byte("test"))
	assert.NoError(t, err, "connection should still be usable when setConn is called with the same connection")
}

func TestGetSetConn_ConcurrentAccess(t *testing.T) {
	e := newTestEventHandler("")
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			e.setConn(client)
		}()
		go func() {
			defer wg.Done()
			_ = e.getConn()
		}()
	}
	wg.Wait()
	// No race detector failures = success; use assert to satisfy unused-parameter lint.
	assert.NotNil(t, e)
}

// --- signalBrokenPipe ---

func TestSignalBrokenPipe_SendsSignal(t *testing.T) {
	e := newTestEventHandler("")
	e.signalBrokenPipe()

	select {
	case <-e.brokenPipeCh:
		// expected
	default:
		t.Fatal("expected signal on brokenPipeCh")
	}
}

func TestSignalBrokenPipe_NonBlocking(t *testing.T) {
	e := newTestEventHandler("")

	// Fill the channel.
	e.brokenPipeCh <- struct{}{}

	// Second signal should not block.
	done := make(chan struct{})
	go func() {
		e.signalBrokenPipe()
		close(done)
	}()

	select {
	case <-done:
		// expected: did not block
	case <-time.After(1 * time.Second):
		t.Fatal("signalBrokenPipe blocked when channel was already full")
	}
}

// --- writeLogToSocket ---

func TestWriteLogToSocket_NilConnection(t *testing.T) {
	e := newTestEventHandler("")
	// conn is nil by default
	result := e.writeLogToSocket("test message\n")
	assert.False(t, result, "should return false when connection is nil")
}

func TestWriteLogToSocket_SuccessfulWrite(t *testing.T) {
	e := newTestEventHandler("")

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	e.setConn(client)

	msg := "test message\n"
	go func() {
		buf := make([]byte, len(msg))
		_, _ = server.Read(buf)
	}()

	result := e.writeLogToSocket(msg)
	assert.True(t, result, "should return true on successful write")
	assert.NotNil(t, e.getConn(), "connection should still be set after successful write")
}

func TestWriteLogToSocket_BrokenPipeClearsConn(t *testing.T) {
	// Create a socket pair and close the server side to simulate broken pipe.
	server, client := net.Pipe()
	server.Close() // close server so writes fail

	// Use an invalid socket path so reconnect fails.
	// Send shutdown signal shortly after to abort reconnect backoff quickly.
	e := newTestEventHandler("/nonexistent/test.sock")
	e.setConn(client)

	go func() {
		time.Sleep(50 * time.Millisecond)
		e.closeCh <- true
	}()

	result := e.writeLogToSocket("test message\n")
	assert.False(t, result, "should return false when write fails and reconnect is not possible")
	assert.Nil(t, e.getConn(), "connection should be nil after failed write")
}

// --- reconnectEventSocket ---

func TestReconnectEventSocket_SuccessWithUnixSocket(t *testing.T) {
	// Create a temporary Unix domain socket with short path.
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	go acceptAndClose(listener)

	e := newTestEventHandler(socketPath)
	result := e.reconnectEventSocket()
	assert.True(t, result, "should succeed when socket is available")
	assert.NotNil(t, e.getConn(), "connection should be set after successful reconnect")
	e.setConn(nil) // cleanup
}

func TestReconnectEventSocket_FailsWithNoSocket(t *testing.T) {
	e := newTestEventHandler("/nonexistent/path/test.sock")

	// Send shutdown signal shortly after to abort reconnect backoff quickly.
	go func() {
		time.Sleep(50 * time.Millisecond)
		e.closeCh <- true
	}()

	result := e.reconnectEventSocket()
	assert.False(t, result, "should fail when socket path does not exist")
	assert.Nil(t, e.getConn(), "connection should remain nil")
}

func TestReconnectEventSocket_ReturnsExistingConnection(t *testing.T) {
	e := newTestEventHandler("")
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	e.setConn(client)
	result := e.reconnectEventSocket()
	assert.True(t, result, "should return true when connection already exists")
	assert.Equal(t, client, e.getConn(), "existing connection should be unchanged")
}

func TestReconnectEventSocket_RespondsToShutdown(t *testing.T) {
	e := newTestEventHandler("/nonexistent/path/test.sock")

	// Signal shutdown before calling reconnect.
	go func() {
		time.Sleep(10 * time.Millisecond)
		e.closeCh <- true
	}()

	start := time.Now()
	result := e.reconnectEventSocket()
	elapsed := time.Since(start)

	assert.False(t, result, "should return false when shutdown signal received")
	assert.Less(t, elapsed, 3*time.Second, "should return quickly on shutdown signal")
}

func TestReconnectEventSocket_Serialized(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	go acceptAndHold(listener)

	e := newTestEventHandler(socketPath)
	var wg sync.WaitGroup
	results := make([]bool, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = e.reconnectEventSocket()
		}(i)
	}
	wg.Wait()

	// All should succeed (via reconnect or early return since conn is already set).
	for i, r := range results {
		assert.True(t, r, "goroutine %d should succeed", i)
	}
	assert.NotNil(t, e.getConn())
	e.setConn(nil) // cleanup
}

// --- writeLogToSocket with real socket ---

func TestWriteLogToSocket_WriteAndReconnect(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := newTestEventHandler(socketPath)

	// Establish initial connection.
	assert.True(t, e.reconnectEventSocket())

	// Write successfully.
	msg := "hello world\n"
	result := e.writeLogToSocket(msg)
	assert.True(t, result)

	// Verify the message was received.
	select {
	case got := <-received:
		assert.Equal(t, msg, got)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}

	// Simulate broken pipe: close the current connection.
	e.setConn(nil)

	// writeLogToSocket should detect nil conn and return false.
	result = e.writeLogToSocket("should fail\n")
	assert.False(t, result)

	e.setConn(nil) // cleanup
}

// --- EmitProcessStatusLog ---

func TestEmitProcessStatusLog_WritesToSocket(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := newTestEventHandler(socketPath)
	assert.True(t, e.reconnectEventSocket())

	e.EmitProcessStatusLog("ptp4l", "ptp4l.0.config", 1)

	select {
	case got := <-received:
		assert.True(t, strings.Contains(got, "ptp4l"), "message should contain process name")
		assert.True(t, strings.Contains(got, "ptp4l.0.config"), "message should contain config name")
		assert.True(t, strings.Contains(got, "PTP_PROCESS_STATUS:1"), "message should contain status")
		assert.True(t, strings.HasSuffix(got, "\n"), "message should end with newline")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for process status message")
	}

	e.setConn(nil) // cleanup
}

func TestEmitProcessStatusLog_FormatIsCorrect(t *testing.T) {
	e := newTestEventHandler("")
	// No connection; should not panic, just log and return.
	e.EmitProcessStatusLog("ptp4l", "test.config", 0)

	// Verify format by calling with a connection that captures output.
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndReadOnce(listener, received)

	e2 := newTestEventHandler(socketPath)
	assert.True(t, e2.reconnectEventSocket())

	now := time.Now().Unix()
	e2.EmitProcessStatusLog("phc2sys", "phc2sys.0.config", 0)

	select {
	case got := <-received:
		// Verify prefix and suffix without relying on exact timestamp match
		assert.True(t, strings.HasPrefix(got, "phc2sys["), "message should start with process name")
		assert.True(t, strings.HasSuffix(got, "]:[phc2sys.0.config] PTP_PROCESS_STATUS:0\n"), "message should end with config and status")

		// Parse and verify the embedded timestamp is within a reasonable range
		re := regexp.MustCompile(`\[(\d+)\]`)
		matches := re.FindStringSubmatch(got)
		if assert.Len(t, matches, 2, "should contain a bracketed timestamp") {
			timestamp, parseErr := strconv.ParseInt(matches[1], 10, 64)
			assert.NoError(t, parseErr)
			assert.InDelta(t, now, timestamp, 2, "timestamp should be within 2 seconds of test time")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for message")
	}

	e2.setConn(nil) // cleanup
}
