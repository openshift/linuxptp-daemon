package daemon

import (
	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

// NewTestPMCProcess creates a PMCProcess with injectable dependencies for testing.
func NewTestPMCProcess(
	configFileName, clockType string,
	getMonitorFn func(string) (*expect.GExpect, <-chan error, error),
	pollFn func(string, bool) (protocol.ParentDataSet, error),
) *PMCProcess {
	return &PMCProcess{
		configFileName:       configFileName,
		messageTag:           "[" + configFileName + ":{level}]",
		monitorParentData:    true,
		parentDSCh:           make(chan protocol.ParentDataSet, 10),
		clockType:            clockType,
		exitCh:               make(chan struct{}, 1),
		getMonitorFn:         getMonitorFn,
		runPMCExpGetParentDS: pollFn,
	}
}
