package intel

import (
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
