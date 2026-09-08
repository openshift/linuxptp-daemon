package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
)

const processStartTimeout = 10 * time.Second
const processRestartTimeout = processStartTimeout

// ProcessManager manages a set of ptpProcess
// which could be ptp4l, phc2sys or timemaster.
// Processes in ProcessManager will be started
// or stopped simultaneously.
type ProcessManager struct {
	process        []process.Process
	waitingProcess []process.Process
	eventsIn       chan event.Event
	eventsOut      chan event.Event
	forwardOnce    sync.Once
	clockMgr       *clockmgr.ClockManager
	daemon         *Daemon
}

// findProcessesByName returns a list of processes with the given name
func (pm *ProcessManager) findProcessesByName(name string) []process.Process {
	var procs []process.Process
	for _, proc := range pm.process {
		if proc.Name() == name {
			procs = append(procs, proc)
		}
	}
	return procs
}

func startProcessWithRetry(ctx context.Context, p process.Process, timeout time.Duration) error {
	timeoutCh := time.After(timeout)
	var lastErr error
	for retryCount := 0; ; retryCount++ {
		select {
		case <-ctx.Done():
			glog.Infof("exiting start for %s in profile %s, context cancelled", p.Name(), profileName(p))
			return nil
		case <-timeoutCh:
			return fmt.Errorf("failed to start %s after %d attempts: %s", p.Name(), retryCount, lastErr)
		default:
			err := p.Start(ctx)
			if err != nil {
				lastErr = err
				glog.Errorf("failed to start process %s: %s, retrying...", p.Name(), err)
				continue
			}
			return nil
		}
	}
}

func profileName(p process.Process) string {
	if p == nil {
		return ""
	}
	prof := p.Profile()
	if prof == nil || prof.Name == nil {
		return ""
	}
	return *prof.Name
}

func (pm *ProcessManager) forEachProcess(fn func(process.Process)) {
	for _, p := range pm.process {
		for _, d := range p.DependentProcesses() {
			fn(d)
		}
		fn(p)
	}
}

func (pm *ProcessManager) startOne(ctx context.Context, p process.Process) {
	if err := startProcessWithRetry(ctx, p, processStartTimeout); err != nil {
		glog.Errorf("Failed to start process: %s", err)
		return
	}
	if pm.daemon != nil {
		if profile := p.Profile(); profile != nil {
			pm.daemon.pluginManager.AfterRunPTPCommand(profile, p.Name())
		}
		// ts2phc start is now driven by conditions, not by explicit event emission
	}
	if syncer, ok := p.(initialStateSyncer); ok {
		syncer.SyncInitialState()
	}
}

// StartProcesses initiates the event forwarding and starts processes with Immediate conditions.
func (pm *ProcessManager) StartProcesses(ctx context.Context) {
	immediate := process.Immediate{}

	pm.forEachProcess(func(p process.Process) {
		if p.State() != process.Created && p.State() != process.Stopped {
			return
		}
		cond := process.GetCondition(p, process.ActionStart, immediate)
		if _, imm := cond.(process.Immediate); imm {
			pm.startOne(ctx, p)
			return
		}
		glog.Infof("ProcessManager: waiting to start %s until %s", p.Name(), cond)
	})
	pm.waitingProcess = pm.getWaitingProcesses()

	pm.forwardOnce.Do(func() {
		go pm.forwardEvents(ctx)
	})
}

func (pm *ProcessManager) forwardEvents(ctx context.Context) {
	if pm.waitingProcess == nil {
		pm.waitingProcess = pm.getWaitingProcesses()
	}
	for {
		select {
		case <-ctx.Done():
			glog.V(20).Info("ProcessManager: event forwarder stopped (context cancelled)")
			return
		case ev, ok := <-pm.eventsIn:
			if !ok {
				glog.V(20).Info("ProcessManager: event forwarder stopped (inbound closed)")
				return
			}
			ps, isPS := ev.Data.(*event.ProcessStatusData)
			if isPS && ps.Status == PtpProcessDown {
				pm.handleProcessDown(ctx, ev)
			}
			pm.evalActions(ctx, ev)
			if !isPS {
				pm.eventsOut <- ev
			}
		}
	}
}

func waitingOnCondition(p process.Process) bool {
	switch p.State() {
	case process.Created:
		return nil != process.GetCondition(p, process.ActionStart, nil)
	case process.Stopping, process.Stopped, process.Dead:
		return nil != process.GetCondition(p, process.ActionRestart, nil)
	case process.Starting, process.Running:
		if nil != process.GetCondition(p, process.ActionStop, nil) {
			return true
		}

		if enabler, ok := p.(process.Enabler); !ok {
			return false
		} else if enabler.IsEnabled() {
			return nil != process.GetCondition(p, process.ActionDisable, nil)
		}
		return nil != process.GetCondition(p, process.ActionEnable, nil)
	}
	return false
}

func (pm *ProcessManager) getWaitingProcesses() []process.Process {
	var waiting []process.Process
	pm.forEachProcess(func(p process.Process) {
		if waitingOnCondition(p) {
			waiting = append(waiting, p)
		}
	})
	return waiting
}

// collectWindowRequests gathers all config names required by conditions on waiting processes.
func (pm *ProcessManager) collectWindowRequests() []process.WindowRequest {
	seen := make(map[process.WindowRequest]bool)
	var result []process.WindowRequest

	for _, p := range pm.waitingProcess {
		if p == nil {
			continue
		}
		conds := p.Conditions()
		if conds == nil {
			continue
		}
		for _, cond := range conds {
			if cond == nil {
				continue
			}
			for _, cfg := range cond.GetWindowRequests() {
				if !seen[cfg] {
					seen[cfg] = true
					result = append(result, cfg)
				}
			}
		}
	}
	return result
}

func (pm *ProcessManager) evalActions(ctx context.Context, ev event.Event) {
	var stats process.EventStats
	if pm.clockMgr != nil {
		windowRequests := pm.collectWindowRequests()
		stats = pm.clockMgr.GetWindows(windowRequests)
	}

	hasChanged := false

	for _, p := range pm.waitingProcess {
		switch p.State() {
		case process.Created:
			if cond := process.GetCondition(p, process.ActionStart, process.Immediate{}); cond.Met(p, ev, stats) {
				pm.startOne(ctx, p)
				hasChanged = true
			} else {
				glog.Infof("ProcessManager: waiting to start %s until %s", p.Name(), cond)
			}
		case process.Dead:
			if cond := process.GetCondition(p, process.ActionRestart, process.Immediate{}); cond.Met(p, ev, stats) {
				pm.startOne(ctx, p)
				hasChanged = true
			} else {
				glog.Infof("ProcessManager: waiting to restart from dead %s until %s", p.Name(), cond)
			}
		case process.Stopped:
			if cond := process.GetCondition(p, process.ActionRestart, process.Never{}); cond.Met(p, ev, stats) {
				pm.startOne(ctx, p)
				hasChanged = true
			} else {
				glog.Infof("ProcessManager: waiting to restart %s until %s", p.Name(), cond)
			}
		case process.Running:
			if cond := process.GetCondition(p, process.ActionStop, process.Never{}); cond.Met(p, ev, stats) {
				glog.V(2).Infof("ProcessManager: stop condition met for %s (%s) on source=%s", p.Name(), cond, ev.Source)
				p.Stop()
				hasChanged = true
				continue
			}
			enabler, ok := p.(process.Enabler)
			if !ok {
				continue
			}
			if enabler.IsEnabled() {
				if cond := process.GetCondition(p, process.ActionDisable, process.Never{}); cond.Met(p, ev, stats) {
					glog.V(2).Infof("ProcessManager: disable condition met for %s (%s) on source=%s", p.Name(), cond, ev.Source)
					enabler.Disable()
					hasChanged = true
				}
				continue
			}
			if cond := process.GetCondition(p, process.ActionEnable, process.Never{}); cond.Met(p, ev, stats) {
				glog.V(2).Infof("ProcessManager: enable condition met for %s (%s) on source=%s", p.Name(), cond, ev.Source)
				enabler.Enable()
				hasChanged = true
				continue
			}
		}
	}
	if hasChanged {
		pm.waitingProcess = pm.getWaitingProcesses()
	}
}

func (pm *ProcessManager) handleProcessDown(ctx context.Context, ev event.Event) {
	p := pm.findProcess(ev)
	if p == nil {
		glog.V(2).Infof("ProcessManager: process_status down unmatched source=%s cfg=%s iface=%s",
			ev.Source, ev.CfgName, ev.IFace)
		return
	}
	if p.State() != process.Dead {
		glog.V(2).Infof("ProcessManager: process_status down ignored for %s state=%s source=%s",
			p.Name(), p.State(), ev.Source)
		return
	}
	select {
	case <-ctx.Done():
		return
	default:
	}
	glog.Infof("restarting dead process %s", p.Name())
	if err := startProcessWithRetry(ctx, p, processRestartTimeout); err != nil {
		glog.Errorf("failed to restart %s: %v", p.Name(), err)
	}
}

func (pm *ProcessManager) findProcess(ev event.Event) process.Process {
	for _, p := range pm.process {
		if p.Name() == string(ev.Source) && p.ConfigName() == ev.CfgName {
			return p
		}
		for _, d := range p.DependentProcesses() {
			if d.Name() == string(ev.Source) && d.ConfigName() == ev.CfgName {
				return d
			}
		}
	}
	return nil
}

func sendProcessStatusEvent(ch chan<- event.Event, source event.EventSource, cfgName string, clockType event.ClockType, iface string, status int64) {
	if ch == nil {
		return
	}
	ch <- event.ProcessStatusEvent(source, cfgName, clockType, iface, status)
}

func (pm *ProcessManager) stopAllProcesses() {
	for _, p := range pm.process {
		glog.Infof("stopping process.... %s", p.Name())
		depProcesses := p.DependentProcesses()
		for i := len(depProcesses) - 1; i >= 0; i-- {
			d := depProcesses[i]
			glog.Infof("Stopping %s", d.Name())
			d.Stop()
		}

		glog.Infof("Stopping %s", p.Name())
		p.Stop()
		if p, ok := p.(*ptpProcess); ok {
			p.depProcess = nil
			p.hasCollectedMetrics = false

			// Cleanup metrics
			deleteMetrics(p.ifaces, p.haProfile, p.name, p.configName, p.messageTag)

			if p.name == syncEProcessName && p.syncERelations != nil {
				deleteSyncEMetrics(p.name, p.configName, p.syncERelations)
			}
		}

		glog.Infof("Stopped %s", p.Name())
	}
}

// pendingDelayedStart is true when the process has not been started yet and
// its ActionStart condition is not Immediate (e.g. delayed phc2sys).
func pendingDelayedStart(p process.Process) bool {
	if p == nil {
		return false
	}
	// Check if the process has a non-Immediate start condition.
	// If it does, it's intentionally delayed and should be skipped in readiness checks.
	_, immediate := process.GetCondition(p, process.ActionStart, process.Immediate{}).(process.Immediate)
	return !immediate
}
