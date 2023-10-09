package pmc

import (
	"fmt"
	"syscall"

	"regexp"
	"time"

	"github.com/golang/glog"
	expect "github.com/google/goexpect"
)

var (
	ClockClassChangeRegEx = regexp.MustCompile(`gm.ClockClass[[:space:]]+(\d+)`)
	CmdParentDataSet      = "GET PARENT_DATA_SET"
	cmdTimeout            = 500 * time.Millisecond
	sigTimeout            = 500 * time.Millisecond
)

// RunPMCExp ... go expect to run PMC util cmd
func RunPMCExp(configFileName, cmdStr string, promptRE *regexp.Regexp) (result string, matches []string, err error) {
	pmcCmd := fmt.Sprintf("pmc -u -b 0 -f /var/run/%s", configFileName)
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return "", []string{}, err
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	if err = e.Send(cmdStr + "\n"); err == nil {
		result, matches, err = e.Expect(promptRE, cmdTimeout)
		if err != nil {
			glog.Errorf("pmc result match error %s", err)
			return
		}
	}
	return
}
