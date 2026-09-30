package daemon

import (
	"fmt"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/addons/generic"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
)

type chronydProcess struct {
	*ptpProcess
}

// NewChronydProcess creates a new chronyd process instance.
func NewChronydProcess(env ptpProcessEnv) (*chronydProcess, error) {
	if !env.hasFailover {
		return nil, fmt.Errorf("chronyd process only needed when we have ntpFailover")
	}

	configFile := fmt.Sprintf("chronyd.%d.config", env.runID)
	configPath := fmt.Sprintf("%s/%s", configPrefix, configFile)
	messageTag := fmt.Sprintf("[chronyd.%d.config]", env.runID)
	p := newPtpProcess(chronydProcessName, configFile, configPath, "", messageTag, env.nodeProfile, env.clockType, env.dn)
	output, opts, err := p.loadProcessConf(env)
	if err != nil {
		return nil, err
	}
	output.setPtp4lConfOption("", "bindcmdaddress", ChronydSocketPath, true)
	output.profile_name = *env.nodeProfile.Name
	p.addMonitorFlags(output, opts)
	var configOutput string
	configOutput, p.ifaces = output.RenderPtp4lConf()
	if err = p.writeProcessConf(output, configOutput); err != nil {
		return nil, err
	}
	p.cmd = buildCmd(buildPtpCmdLine(chronydProcessName, configPath, opts, env.nodeProfile))
	cp := &chronydProcess{ptpProcess: p}
	cp.conditions = map[process.Action]process.Condition{
		process.ActionStart: process.OnPluginEvent{EventName: generic.GnssFailoverEventName},
		process.ActionStop:  process.OnPluginEvent{EventName: generic.GnssRecoveredEventName},
	}
	return cp, nil
}
