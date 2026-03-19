package ublox

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_CmdDisableNmeaMsg(t *testing.T) {
	expected := []string{
		"CFG-MSGOUT-NMEA_ID_FOO_I2C,0",
		"CFG-MSGOUT-NMEA_ID_FOO_UART1,0",
		"CFG-MSGOUT-NMEA_ID_FOO_UART2,0",
		"CFG-MSGOUT-NMEA_ID_FOO_USB,0",
		"CFG-MSGOUT-NMEA_ID_FOO_SPI,0",
	}
	found := make([]string, 0, len(expected))
	cmds := cmdDisableNmeaMsg("FOO")
	assert.Equal(t, len(expected), len(cmds))
	for _, cmd := range cmds {
		assert.Equal(t, "-z", cmd[0])
		if slices.Contains(expected, cmd[1]) {
			found = append(found, cmd[1])
		}
	}
	slices.Sort(expected)
	slices.Sort(found)
	assert.Equal(t, expected, found)
}
