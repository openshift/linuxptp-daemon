package utils

import (
	"fmt"
	"net"
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

// EmitClockClass writes a clock class change event to log and the network connection if provided.
func EmitClockClass(c net.Conn, name string, configName string, clockClass fbprotocol.ClockClass) {
	glog.Info(GetClockClassLogMessage(name, configName, clockClass))
	if c == nil {
		return
	}

	_, err := c.Write([]byte(GetClockClassLogMessage(name, configName, clockClass)))
	if err != nil {
		glog.Errorf("failed to write class change event to socket: %s", err.Error())
	}
}
