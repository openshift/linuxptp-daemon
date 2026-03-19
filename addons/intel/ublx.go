package intel

import (
	"os/exec"
	"slices"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
)

// UblxCmdList is a list of UblxCmd items
type UblxCmdList []UblxCmd

// UblxCmd represents a single ublox command
type UblxCmd struct {
	ReportOutput bool     `json:"reportOutput"`
	Args         []string `json:"args"`
}

// ublxSaveCommand saves the current configuration
var ublxSaveCommand = UblxCmd{
	ReportOutput: false,
	Args:         []string{"-p", "SAVE"},
}

// A named anonymous function for ease of mocking
var execCombined = func(name string, arg ...string) ([]byte, error) {
	return exec.Command(name, arg...).CombinedOutput()
}

// run a single UblxCmd and return the result
func (cmd UblxCmd) run() (string, error) {
	args := cmd.Args
	if !slices.Contains(cmd.Args, "-w") {
		// Default wait time is 2s per command; prefer 0.1s
		args = append([]string{"-w", "0.1"}, args...)
	}
	glog.Infof("Running ubxtool with: %s", strings.Join(args, ", "))
	stdout, err := execCombined(ublox.UBXCommand, args...)
	return string(stdout), err
}

// run a set of UblxCmds, returning the output of any that have 'ReportOutput' set.
func (cmdList UblxCmdList) runAll(withSave bool) []string {
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
	if withSave {
		_, err := ublxSaveCommand.run()
		if err != nil {
			glog.Warningf("ubxtool SAVE error: %s", err)
		}
	}
	return results
}
