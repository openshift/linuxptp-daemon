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

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/stretchr/testify/assert"
)

// newTestEventHandler creates a minimal EventHandler for testing socket logic.
func newTestEventHandler(socketPath string) *EventHandler {
	return &EventHandler{
		stdoutSocket:   socketPath,
		stdoutToSocket: true,
		closeCh:        make(chan bool, 1),
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

	// Accept the reconnection attempt so the write can succeed.
	go acceptAndRead(listener, received)

	// writeLogToSocket should reconnect and succeed since listener is still up.
	result = e.writeLogToSocket("test message\n")
	assert.True(t, result)

	select {
	case got := <-received:
		assert.Equal(t, "test message\n", got)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reconnected message")
	}

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

// --- EmitPortRoleLogs ---

func TestEmitPortRoleLogs_AppendsNewline(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := newTestEventHandler(socketPath)
	assert.True(t, e.reconnectEventSocket())

	// Set port role events without trailing newlines (as parser produces them)
	e.SetPortRole("ptp4l.0.config", "ens3f2", &parser.PTPEvent{
		Raw: "ptp4l[137078.691]: [ptp4l.0.config:5] port 1 (ens3f2): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
	})
	e.SetPortRole("ptp4l.0.config", "ens3f1", &parser.PTPEvent{
		Raw: "ptp4l[137078.515]: [ptp4l.0.config:5] port 2 (ens3f1): LISTENING to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES",
	})

	e.EmitPortRoleLogs()

	// Collect all data from the socket (may arrive in one or multiple reads)
	var allData strings.Builder
	timeout := time.After(2 * time.Second)
	for {
		select {
		case got := <-received:
			allData.WriteString(got)
			// Check if we have all expected lines
			lines := strings.Split(strings.TrimSuffix(allData.String(), "\n"), "\n")
			if len(lines) >= 2 {
				goto done
			}
		case <-timeout:
			t.Fatalf("timed out waiting for port role messages, got: %q", allData.String())
		}
	}
done:

	// Split by newline and verify each line is a separate, parseable log entry
	combined := allData.String()
	assert.True(t, strings.HasSuffix(combined, "\n"), "combined output should end with newline")
	lines := strings.Split(strings.TrimSuffix(combined, "\n"), "\n")
	assert.Equal(t, 2, len(lines), "should have exactly 2 newline-separated lines")
	for i, line := range lines {
		assert.Contains(t, line, "ptp4l.0.config", "line %d should contain config name", i)
		assert.NotEmpty(t, line, "line %d should not be empty", i)
	}

	e.setConn(nil) // cleanup
}

func TestEmitPortRoleLogs_PreservesExistingNewline(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := newTestEventHandler(socketPath)
	assert.True(t, e.reconnectEventSocket())

	// Set a port role event that already has a trailing newline
	e.SetPortRole("ptp4l.0.config", "ens3f2", &parser.PTPEvent{
		Raw: "ptp4l[137078.691]: [ptp4l.0.config:5] port 1 (ens3f2): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED\n",
	})

	e.EmitPortRoleLogs()

	select {
	case got := <-received:
		// Should have exactly one trailing newline, not double
		assert.True(t, strings.HasSuffix(got, "\n"), "message should end with newline")
		assert.False(t, strings.HasSuffix(got, "\n\n"), "message should not have double newline")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for port role message")
	}

	e.setConn(nil) // cleanup
}

// --- EmitClockSyncLogs ---

func TestEmitClockSyncLogs_WritesWithNewline(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := newTestEventHandler(socketPath)
	assert.True(t, e.reconnectEventSocket())

	// Set clock sync state with logs that already have trailing newlines (as GetLogData produces)
	e.Lock()
	e.clkSyncState["ptp4l.0.config"] = &clockSyncState{
		clkLog: "GM[1710000000]:[ptp4l.0.config] ens3f2 T-GM-STATUS s2\n",
	}
	e.clkSyncState["ptp4l.1.config"] = &clockSyncState{
		clkLog: "GM[1710000001]:[ptp4l.1.config] ens2f0 T-GM-STATUS s2\n",
	}
	e.Unlock()

	e.EmitClockSyncLogs()

	// Collect all data from the socket
	var allData strings.Builder
	timeout := time.After(2 * time.Second)
	for {
		select {
		case got := <-received:
			allData.WriteString(got)
			lines := strings.Split(strings.TrimSuffix(allData.String(), "\n"), "\n")
			if len(lines) >= 2 {
				goto done
			}
		case <-timeout:
			t.Fatalf("timed out waiting for clock sync messages, got: %q", allData.String())
		}
	}
done:

	combined := allData.String()
	assert.True(t, strings.HasSuffix(combined, "\n"), "combined output should end with newline")
	lines := strings.Split(strings.TrimSuffix(combined, "\n"), "\n")
	assert.Equal(t, 2, len(lines), "should have exactly 2 newline-separated lines")
	for i, line := range lines {
		assert.Contains(t, line, "T-GM-STATUS", "line %d should contain T-GM-STATUS", i)
	}

	e.setConn(nil) // cleanup
}

// --- emitClockClass ---

func TestEmitClockClass_WritesToSocket(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := newTestEventHandler(socketPath)
	assert.True(t, e.reconnectEventSocket())

	e.emitClockClass(fbprotocol.ClockClass(6), "ptp4l.0.config")

	select {
	case got := <-received:
		assert.Contains(t, got, "CLOCK_CLASS_CHANGE 6", "message should contain clock class value")
		assert.Contains(t, got, "ptp4l.0.config", "message should contain config name")
		assert.Contains(t, got, "ptp4l", "message should contain process name")
		assert.True(t, strings.HasSuffix(got, "\n"), "message should end with newline")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for clock class message")
	}

	e.setConn(nil) // cleanup
}

func TestEmitClockClass_NilConnectionNoWrite(_ *testing.T) {
	e := newTestEventHandler("")
	// conn is nil by default; signal closeCh so reconnect exits immediately
	e.closeCh <- true
	// should not panic
	e.emitClockClass(fbprotocol.ClockClass(6), "ptp4l.0.config")
}

func TestEmitClockClass_ReconnectsOnBrokenPipe(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := newTestEventHandler(socketPath)
	assert.True(t, e.reconnectEventSocket())

	// Break the connection by closing it
	e.getConn().Close()
	e.setConn(nil)

	// Re-establish and verify it can write after reconnection
	assert.True(t, e.reconnectEventSocket())
	e.emitClockClass(fbprotocol.ClockClass(248), "ptp4l.7.config")

	select {
	case got := <-received:
		assert.Contains(t, got, "CLOCK_CLASS_CHANGE 248")
		assert.Contains(t, got, "ptp4l.7.config")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for clock class message after reconnect")
	}

	e.setConn(nil) // cleanup
}

// --- EmitClockClass (exported, via clkSyncState) ---

func TestEmitClockClassExported_SkipsWhenNoClkSyncState(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := NewEventHandlerForTests(socketPath)
	assert.True(t, e.reconnectEventSocket())

	// No clkSyncState entry for this config; EmitClockClass should return early
	e.EmitClockClass("ptp4l.99.config")

	// Verify nothing was sent
	select {
	case got := <-received:
		t.Fatalf("expected no message but got: %q", got)
	case <-time.After(200 * time.Millisecond):
		// expected: no data sent
	}

	e.setConn(nil) // cleanup
}

func TestEmitClockClassExported_EmitsStoredClockClass(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := NewEventHandlerForTests(socketPath)
	assert.True(t, e.reconnectEventSocket())

	// Populate clkSyncState via test helper
	e.SetClockClass("ptp4l.0.config", 6)
	e.SetClockClass("ptp4l.1.config", 255)

	// Emit for config 0
	e.EmitClockClass("ptp4l.0.config")

	select {
	case got := <-received:
		assert.Contains(t, got, "CLOCK_CLASS_CHANGE 6")
		assert.Contains(t, got, "ptp4l.0.config")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for clock class message")
	}

	// Emit for config 1
	e.EmitClockClass("ptp4l.1.config")

	select {
	case got := <-received:
		assert.Contains(t, got, "CLOCK_CLASS_CHANGE 255")
		assert.Contains(t, got, "ptp4l.1.config")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for clock class message for config 1")
	}

	e.setConn(nil) // cleanup
}

// --- storeClockClassLocked ---

func TestStoreClockClassLocked_CreatesNewEntry(t *testing.T) {
	e := newTestEventHandler("")
	e.Lock()
	e.storeClockClassLocked("ptp4l.0.config", fbprotocol.ClockClass(6), fbprotocol.ClockAccuracy(0x21))
	state, ok := e.clkSyncState["ptp4l.0.config"]
	e.Unlock()

	assert.True(t, ok)
	assert.Equal(t, fbprotocol.ClockClass(6), state.clockClass)
	assert.Equal(t, fbprotocol.ClockAccuracy(0x21), state.clockAccuracy)
}

func TestStoreClockClassLocked_UpdatesExistingEntry(t *testing.T) {
	e := newTestEventHandler("")

	// Pre-populate with other fields
	e.clkSyncState["ptp4l.0.config"] = &clockSyncState{
		state:      PTP_LOCKED,
		clockClass: fbprotocol.ClockClass(248),
	}

	e.Lock()
	e.storeClockClassLocked("ptp4l.0.config", fbprotocol.ClockClass(6), fbprotocol.ClockAccuracy(0x21))
	state := e.clkSyncState["ptp4l.0.config"]
	e.Unlock()

	assert.Equal(t, fbprotocol.ClockClass(6), state.clockClass)
	assert.Equal(t, fbprotocol.ClockAccuracy(0x21), state.clockAccuracy)
	// Existing field preserved
	assert.Equal(t, PTP_LOCKED, state.state)
}

func TestStoreClockClassLocked_MakesEmitClockClassWork(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := newTestEventHandler(socketPath)
	assert.True(t, e.reconnectEventSocket())

	// Simulate what clockClassRequestCh handler does
	e.Lock()
	e.storeClockClassLocked("ptp4l.0.config", fbprotocol.ClockClass(6), fbprotocol.ClockAccuracy(0x21))
	e.storeClockClassLocked("ptp4l.1.config", fbprotocol.ClockClass(255), fbprotocol.ClockAccuracy(0xFE))
	e.Unlock()

	// EmitClockClass should now find and emit the stored values
	e.EmitClockClass("ptp4l.0.config")

	select {
	case got := <-received:
		assert.Contains(t, got, "CLOCK_CLASS_CHANGE 6")
		assert.Contains(t, got, "ptp4l.0.config")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for clock class message")
	}

	e.EmitClockClass("ptp4l.1.config")

	select {
	case got := <-received:
		assert.Contains(t, got, "CLOCK_CLASS_CHANGE 255")
		assert.Contains(t, got, "ptp4l.1.config")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for clock class message")
	}

	e.setConn(nil) // cleanup
}

// --- announceClockClass stores in clkSyncState ---

func TestAnnounceClockClass_PopulatesClkSyncState(t *testing.T) {
	socketPath := shortSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	received := make(chan string, 10)
	go acceptAndRead(listener, received)

	e := newTestEventHandler(socketPath)
	assert.True(t, e.reconnectEventSocket())

	// clkSyncState should be empty initially
	e.Lock()
	_, ok := e.clkSyncState["ptp4l.1.config"]
	e.Unlock()
	assert.False(t, ok, "clkSyncState should be empty before announceClockClass")

	// announceClockClass should populate clkSyncState AND emit to socket
	e.announceClockClass(fbprotocol.ClockClass(6), fbprotocol.ClockAccuracy(0x21), "ptp4l.1.config")

	// Verify clkSyncState was populated
	e.Lock()
	state, ok := e.clkSyncState["ptp4l.1.config"]
	e.Unlock()
	assert.True(t, ok, "clkSyncState should have ptp4l.1.config after announceClockClass")
	assert.Equal(t, fbprotocol.ClockClass(6), state.clockClass)
	assert.Equal(t, fbprotocol.ClockAccuracy(0x21), state.clockAccuracy)

	// Verify it was written to socket
	select {
	case got := <-received:
		assert.Contains(t, got, "CLOCK_CLASS_CHANGE 6")
		assert.Contains(t, got, "ptp4l.1.config")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for clock class message on socket")
	}

	e.setConn(nil) // cleanup
}
