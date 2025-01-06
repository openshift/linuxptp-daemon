package daemon

import "github.com/openshift/linuxptp-daemon/pkg/config"
import "net"

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
