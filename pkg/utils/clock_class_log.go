package utils

import (
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
)

// GetClockClassLogMessage formats a clock class change message with timestamp.
func GetClockClassLogMessage(name, configName string, clockClass fbprotocol.ClockClass) string {
	return fmt.Sprintf(
		"%s[%d]:[%s] CLOCK_CLASS_CHANGE %d\n",
		name, time.Now().Unix(), configName, clockClass,
	)
}

// IsBrokenPipe checks if the error indicates a broken pipe or connection issue
func IsBrokenPipe(err error) bool {
	if err == nil {
		return false
	}

	// Check for common connection errors
	if errors.Is(err, syscall.EPIPE) || // Broken pipe
		errors.Is(err, syscall.ECONNRESET) || // Connection reset by peer
		errors.Is(err, syscall.ECONNREFUSED) || // Connection refused
		errors.Is(err, syscall.ENOTCONN) { // Not connected
		return true
	}

	// Check for net.OpError wrapping these errors
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return IsBrokenPipe(opErr.Err)
	}

	return false
}

// socketWriteTimeout is the maximum time to wait for a write to the event socket.
const socketWriteTimeout = 5 * time.Second

// EmitClockClass writes a clock class change event to log and the network connection if provided.
// Returns true if a broken pipe error occurred (caller should reconnect and retry).
func EmitClockClass(c net.Conn, name string, configName string, clockClass fbprotocol.ClockClass) bool {
	logMsg := GetClockClassLogMessage(name, configName, clockClass)
	glog.Info(logMsg)
	if c == nil {
		return false
	}

	if err := c.SetWriteDeadline(time.Now().Add(socketWriteTimeout)); err != nil {
		glog.Warningf("Failed to set write deadline for clock class event: %v", err)
	}
	_, err := c.Write([]byte(logMsg))
	if err != nil {
		glog.Errorf("failed to write class change event to socket: %s", err.Error())
		return IsBrokenPipe(err)
	}
	return false
}
