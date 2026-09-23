package daemon

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
)

type process interface {
	Name() string
	Stopped() bool
	CmdStop()
	CmdInit()
	ProcessStatus(status int64)
	CmdRun()
	MonitorProcess(p config.ProcessConfig)
	ExitCh() chan struct{}
}
