package intel

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/golang/glog"
)

// UblxCmdList is a list of UblxCmd items
type UblxCmdList []UblxCmd

// UblxCmd represents a single ublox command
type UblxCmd struct {
	ReportOutput bool     `json:"reportOutput"`
	Args         []string `json:"args"`
}

// A named anonymous function for ease of mocking
var execCombined = func(name string, arg ...string) ([]byte, error) {
	return exec.Command(name, arg...).CombinedOutput()
}

var (
	// Ublx command to output NAV-CLOCK every second
	cfgMsgEnableNavClock = UblxCmd{
		ReportOutput: false,
		Args:         []string{"-p", "CFG-MSG,1,34,1"},
	}
	// Ublx command to output NAV-STATUS every second
	cfgMsgEnableNavStatus = UblxCmd{
		ReportOutput: false,
		Args:         []string{"-p", "CFG-MSG,1,3,1"},
	}
	// Ublx command to disable SA messages
	cfgMsgDisableSA = UblxCmd{
		ReportOutput: false,
		Args:         []string{"-p", "CFG-MSG,0xf0,0x02,0"},
	}
	// Ublx command to disable SV messages
	cfgMsgDisableSV = UblxCmd{
		ReportOutput: false,
		Args:         []string{"-p", "CFG-MSG,0xf0,0x03,0"},
	}
	// Ublx command to save configuration to storage
	cfgSave = UblxCmd{
		ReportOutput: false,
		Args:         []string{"-p", "SAVE"},
	}

	// All possibel NMEA bus types (according to ubxtool)
	nmeaBusTypes = []string{
		"I2C", "UART1", "UART2", "USB", "SPI",
	}

	// The specific NMEA messages we disable by default
	nmeaDisableMsg = []string{
		"VTG", "GST", "ZDA", "GBS",
	}
)

// Generates a series of UblxCmds which disable the given message type on all bus types
func cmdDisableNmeaMsg(msg string) UblxCmdList {
	result := make(UblxCmdList, len(nmeaBusTypes))
	for i, bus := range nmeaBusTypes {
		result[i] = UblxCmd{Args: []string{"-z", fmt.Sprintf("CFG-MSGOUT-NMEA_ID_%s_%s,0", msg, bus)}}
	}
	return result
}

func defaultUblxCmds() UblxCmdList {
	cmds := UblxCmdList{
		cfgMsgEnableNavClock, cfgMsgEnableNavStatus, cfgMsgDisableSA, cfgMsgDisableSV,
	}
	for _, msg := range nmeaDisableMsg {
		cmds = append(cmds, cmdDisableNmeaMsg(msg)...)
	}
	cmds = append(cmds, cfgSave)
	return cmds
}

// run a single UblxCmd and return the result
func (cmd UblxCmd) run() (string, error) {
	glog.Infof("Running ubxtool with: %s", strings.Join(cmd.Args, ", "))
	stdout, err := execCombined("/usr/local/bin/ubxtool", cmd.Args...)
	return string(stdout), err
}

// run a set of UblxCmds, returning the output of any that have 'ReportOutput' set.
func (cmdList UblxCmdList) runAll() []string {
	results := []string{}
	for _, cmd := range cmdList {
		result, err := cmd.run()
		if err != nil {
			glog.Warningf("ubxtool error: %s", err)
		}
		if cmd.ReportOutput {
			if err != nil {
				results = append(results, err.Error())
			} else {
				glog.Infof("Saving status to hwconfig: %s", result)
				results = append(results, result)
			}
		} else if err != nil {
			glog.Infof("Not saving status to hwconfig: %s", result)
		}
	}
	return results
}
