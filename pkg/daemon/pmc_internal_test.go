package daemon

import (
	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// NewTestPMCProcess creates a PMCProcess with injectable dependencies for testing.
func NewTestPMCProcess(
	configFileName string, clockType event.ClockType,
	eventCh chan<- event.Event,
	getMonitorFn func(string) (*expect.GExpect, <-chan error, error),
) *PMCProcess {
	return &PMCProcess{
		configFileName:    configFileName,
		messageTag:        "[" + configFileName + ":{level}]",
		monitorParentData: true,
		parentDSCh:        make(chan protocol.ParentDataSet, 10),
		eventCh:           eventCh,
		clockType:         clockType,
		profile:           &ptpv1.PtpProfile{},
		getMonitorFn:      getMonitorFn,
	}
}
