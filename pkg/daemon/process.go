package daemon

import "net"
import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"

type process interface {
	Name() string
	Stopped() bool
	CmdStop()
	CmdInit()
	ProcessStatus(c *net.Conn, status int64)
	CmdRun(stdToSocket bool)
	MonitorProcess(p config.ProcessConfig)
	ExitCh() chan struct{}
}
