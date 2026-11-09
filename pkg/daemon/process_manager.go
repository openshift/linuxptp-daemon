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
const waitingProcessReconcileInterval = 10 * time.Second

// ProcessManager manages a set of ptpProcess
// which could be ptp4l, phc2sys or timemaster.
// Processes in ProcessManager will be started
// or stopped simultaneously.
type ProcessManager struct {
	process        []process.Process
	waitingProcess []process.Process
	eventsIn       chan event.Event
	eventsOut      chan event.Event
	statsIn        <-chan process.EventStats
	latestStats    process.EventStats // updated only from the processed-snapshot channel
	processOnce    sync.Once
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
			glog.Infof("exiting start for %s in profile %s, context cancelled", p.Name(), getProfileName(p.Profile()))
			return nil
		case <-timeoutCh:
			return fmt.Errorf("failed to start %s after %d attempts: %s", p.Name(), retryCount, lastErr)
		default:
			err := p.Start(ctx)
			if err != nil {
				lastErr = err
				glog.Errorf("failed to start process %s: %s, retrying...", p.Name(), err)
				time.Sleep(500 * time.Millisecond)
				continue
			}
			return nil
		}
	}
}

func (pm *ProcessManager) forEachProcess(fn func(process.Process)) {
	for _, p := range pm.process {
		for _, d := range p.DependentProcesses() {
			fn(d)
		}
		fn(p)
	}
}

func (pm *ProcessManager) startProcess(ctx context.Context, p process.Process) {
	if err := startProcessWithRetry(ctx, p, processStartTimeout); err != nil {
		glog.Errorf("Failed to start process: %s", err)
		return
	}
	if pm.daemon != nil {
		if profile := p.Profile(); profile != nil {
			pm.daemon.pluginManager.AfterRunPTPCommand(profile, p.Name())
		}
	}
	p.SyncInitialState()
}

// StartProcesses initiates the event forwarding and starts processes with Immediate conditions.
func (pm *ProcessManager) StartProcesses(ctx context.Context) {
	immediate := process.Immediate{}

	// Register condition-waiting processes before starting any immediate
	// processes: immediate starts emit process-status events from their own
	// goroutines, and waiters must already be registered when those events
	// are dispatched, otherwise they wait forever for an event that never
	// re-fires (e.g. pmc waiting for a ptp4l up event that was emitted
	// before the waiter was registered).
	pm.waitingProcess = pm.getWaitingProcesses()

	pm.forEachProcess(func(p process.Process) {
		if p.State() != process.Created && p.State() != process.Stopped {
			return
		}
		cond := process.GetCondition(p, process.ActionStart, immediate)
		if _, imm := cond.(process.Immediate); imm {
			pm.startProcess(ctx, p)
			return
		}
		glog.Infof("ProcessManager: waiting to start %s until %s", p.Name(), cond)
	})
	pm.waitingProcess = pm.getWaitingProcesses()

	pm.processOnce.Do(func() {
		go pm.processEvents(ctx)
	})
}

func (pm *ProcessManager) processEvents(ctx context.Context) {
	if pm.waitingProcess == nil {
		pm.waitingProcess = pm.getWaitingProcesses()
	}
	reconcileTicker := time.NewTicker(waitingProcessReconcileInterval)
	defer reconcileTicker.Stop()
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
			if isPS {
				if p := pm.findProcess(ev); p != nil {
					UpdateProcessStatusMetrics(p.Name(), p.ConfigName(), ps.Status)
				}
				if ps.Status == PtpProcessDown {
					pm.handleProcessDown(ctx, ev)
				}
			} else {
				select {
				case pm.eventsOut <- ev:
				case <-ctx.Done():
					return
				}
			}
			pm.evalActions(ctx, ev)
		case stats, ok := <-pm.statsIn:
			if !ok {
				pm.statsIn = nil
				pm.latestStats = process.EventStats{}
				continue
			}
			pm.latestStats = stats
			pm.reconcileWaiting(ctx)
		case <-reconcileTicker.C:
			pm.reconcileWaiting(ctx)
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
		return nil != process.GetCondition(p, process.ActionStop, nil)
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

// sourceProcessUp reports whether the process with the given name and config
// is currently starting or running. Dependent processes are considered too.
func (pm *ProcessManager) sourceProcessUp(name, cfg string) bool {
	found := false
	pm.forEachProcess(func(p process.Process) {
		if found || p.Name() != name {
			return
		}
		if cfg != "" && p.ConfigName() != cfg {
			return
		}
		switch p.State() {
		case process.Starting, process.Running:
			found = true
		}
	})
	return found
}

// reconcileWaiting evaluates persistent conditions against current process
// state and the latest received clock snapshot. It never queries ClockManager.
// Periodic reconciliation also recovers late or missed process prerequisites.
func (pm *ProcessManager) reconcileWaiting(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	pm.waitingProcess = pm.getWaitingProcesses()
	// A plugin/process event may be selected while a newer snapshot is already
	// queued. Use that pushed value rather than evaluating an older cached state.
	select {
	case stats, ok := <-pm.statsIn:
		if ok {
			pm.latestStats = stats
		} else {
			pm.statsIn = nil
			pm.latestStats = process.EventStats{}
		}
	default:
	}
	hasChanged := false
	for _, p := range pm.waitingProcess {
		if p.State() == process.Starting || p.State() == process.Stopping {
			continue
		}
		action, defaultCondition := stateTransitionAction(p.State())
		cond := process.GetCondition(p, action, defaultCondition)
		sc, ok := cond.(process.StateAwareCondition)
		if !ok {
			continue
		}
		if sc.MetByCurrentState(p, pm.sourceProcessUp, pm.latestStats) {
			switch action {
			case process.ActionStart, process.ActionRestart:
				glog.Infof("ProcessManager: %s condition %s satisfied by current state, starting", p.Name(), cond)
				pm.startProcess(ctx, p)
			case process.ActionStop:
				glog.Infof("ProcessManager: %s stop condition %s satisfied by current state", p.Name(), cond)
				p.Stop()
			}
			hasChanged = true
		}
	}
	if hasChanged {
		pm.waitingProcess = pm.getWaitingProcesses()
	}
}

// stateTransitionAction returns how a process should transition its state, and when we would prefer it to.
func stateTransitionAction(s process.State) (process.Action, process.Condition) {
	switch s {
	case process.Created:
		return process.ActionStart, process.Immediate{}
	case process.Dead:
		return process.ActionRestart, process.Immediate{}
	case process.Stopped:
		return process.ActionRestart, process.Never{}
	case process.Running:
		return process.ActionStop, process.Never{}
	default:
		panic("unhandled default case")
	}
}

func (pm *ProcessManager) evalActions(ctx context.Context, ev event.Event) {
	pm.waitingProcess = pm.getWaitingProcesses()
	if len(pm.waitingProcess) == 0 {
		return
	}

	// This path observes transient process/plugin events (and resets composite
	// event history). Clock readiness is evaluated only during reconciliation,
	// using snapshots pushed after clock processing.
	stats := process.EventStats{}

	hasChanged := false

	for _, p := range pm.waitingProcess {
		if p.State() == process.Starting || p.State() == process.Stopping {
			continue
		}
		action, defaultCondition := stateTransitionAction(p.State())
		switch action {
		case process.ActionStart, process.ActionRestart:
			if cond := process.GetCondition(p, action, defaultCondition); cond.Met(p, ev, stats) {
				pm.startProcess(ctx, p)
				hasChanged = true
			} else {
				glog.Infof("ProcessManager: waiting to (re)start %s until %s", p.Name(), cond)
			}
		case process.ActionStop:
			if cond := process.GetCondition(p, action, defaultCondition); cond.Met(p, ev, stats) {
				glog.V(2).Infof("ProcessManager: stop condition met for %s (%s) on source=%s", p.Name(), cond, ev.Source)
				p.Stop()
				hasChanged = true
			}
		default:
			continue
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
			if p.sourceQualification != nil {
				p.sourceQualification.close()
			}
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
