package daemon

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// NewTestPtpProcess returns a ptpProcess with a caller-supplied cmd.
func NewTestPtpProcess(cmd ProcessCmd, eventCh chan event.Event) *ptpProcess {
	dn := &Daemon{
		processManager: &ProcessManager{eventsIn: eventCh},
	}
	p := newPtpProcess(ptp4lProcessName, "ptp4l.0.config", "", "",
		"[ptp4l.0.config:{level}]", &ptpv1.PtpProfile{},
		event.ClockUnset, dn)
	p.cmd = cmd
	return p
}

// NewTestGpsPipeProcess returns a GpsPipe from NewGpsPipeProcess with a
// caller-supplied cmd and serial-port path (empty serialPort keeps the default).
func NewTestGpsPipeProcess(messageTag string, eventCh chan event.Event, cmd ProcessCmd, serialPort string) *GpsPipe {
	gp := NewGpsPipeProcess(messageTag, &ptpv1.PtpProfile{}, eventCh).(*GpsPipe)
	gp.cmd = cmd
	if serialPort != "" {
		gp.serialPort = serialPort
	}
	return gp
}

// NewTestGpsdProcess returns a GPSD from NewGpsdProcess with a caller-supplied cmd.
func NewTestGpsdProcess(serialPort, gmInterface, messageTag string, eventCh chan event.Event, cmd ProcessCmd) *GPSD {
	g := NewGpsdProcess(serialPort, gmInterface, messageTag, nil, nil, &ptpv1.PtpProfile{}, eventCh, config.ProcessConfig{EventChannel: eventCh}).(*GPSD)
	g.cmd = cmd
	return g
}
