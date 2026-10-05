package daemon

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	pmcPkg "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

const (
	// PMCProcessName is the name identifier for PMC processes
	PMCProcessName = "pmc"
	// defaultMonitorWaitInterval bounds how long the monitor loop will block waiting for a NOTIFY_PARENT_DATA_SET push
	// before it re-polls the parent data set on its own.
	defaultMonitorWaitInterval = 30 * time.Second
)

// NewPMCProcess creates a new PMC process instance for monitoring PTP events.
func NewPMCProcess(runID int, eventCh chan<- event.Event, clockType event.ClockType, nodeProfile *ptpv1.PtpProfile, waitInterval time.Duration) *PMCProcess {
	return &PMCProcess{
		configFileName:    fmt.Sprintf("ptp4l.%d.config", runID),
		messageTag:        fmt.Sprintf("[ptp4l.%d.config:{level}]", runID),
		monitorParentData: true,
		parentDSCh:        make(chan protocol.ParentDataSet, 10),
		eventCh:           eventCh,
		clockType:         clockType,
		waitInterval:      waitInterval,
		profile:           nodeProfile,
		getMonitorFn:      pmcPkg.GetPMCMontior,
		conditions: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{
				Source:     event.PTP4l,
				ConfigName: fmt.Sprintf("ptp4l.%d.config", runID),
			},
		},
	}
}

// PMCProcess manages a PMC (PTP Management Client) process for monitoring PTP events.
type PMCProcess struct {
	lock              sync.RWMutex
	configFileName    string
	monitorPortState  bool
	monitorTimeSync   bool
	monitorParentData bool
	monitorCMLDS      bool
	parentDS          *protocol.ParentDataSet
	parentDSCh        chan protocol.ParentDataSet
	// waitInterval bounds how long expectWorker blocks on Expect before it re-polls the parent data set. This
	// guarantees the clock class metric recovers within one interval even if a NOTIFY_PARENT_DATA_SET push is never
	// delivered (observed on netdevsim after a link outage recovers).
	waitInterval time.Duration
	stopCh       chan struct{}
	clockType    event.ClockType
	messageTag   string
	eventCh      chan<- event.Event
	profile      *ptpv1.PtpProfile

	getMonitorFn func(string) (*expect.GExpect, <-chan error, error)
	processState process.State
	conditions   map[process.Action]process.Condition
	wg           sync.WaitGroup
}

// Name returns the process name.
func (pmc *PMCProcess) Name() string {
	return PMCProcessName
}

// ConfigName ...
func (pmc *PMCProcess) ConfigName() string {
	return pmc.configFileName
}

// SyncInitialState ...
func (pmc *PMCProcess) SyncInitialState() {}

// Conditions returns the action conditions for the PMCProcess.
func (pmc *PMCProcess) Conditions() map[process.Action]process.Condition {
	return pmc.conditions
}

// State returns the current process state of PMCProcess.
func (pmc *PMCProcess) State() process.State {
	pmc.lock.RLock()
	defer pmc.lock.RUnlock()
	return pmc.processState
}

// Profile returns the PTP profile associated with PMCProcess.
func (pmc *PMCProcess) Profile() *ptpv1.PtpProfile {
	return pmc.profile
}

// ClockType returns the clock type of PMCProcess.
func (pmc *PMCProcess) ClockType() event.ClockType {
	return pmc.clockType
}

// DependentProcesses returns processes that depend on PMCProcess.
func (pmc *PMCProcess) DependentProcesses() []process.Process {
	return []process.Process{}
}

func (pmc *PMCProcess) setProcessState(s process.State) {
	pmc.lock.Lock()
	pmc.processState = s
	pmc.lock.Unlock()
}

// Stopped returns whether the process has not been started or has already exited.
func (pmc *PMCProcess) Stopped() bool {
	st := pmc.State()
	return st == process.Created || st == process.Stopped || st == process.Dead
}

// Stop signals the process to stop.
func (pmc *PMCProcess) Stop() error {
	pmc.lock.Lock()
	st := pmc.processState
	if st == process.Created || st == process.Stopping || st == process.Stopped || st == process.Dead {
		pmc.lock.Unlock()
		return nil
	}
	pmc.processState = process.Stopping
	stopCh := pmc.stopCh
	pmc.lock.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	pmc.wg.Wait()
	return nil
}

// ProcessStatus processes status updates for the PMC process.
func (pmc *PMCProcess) ProcessStatus(status int64) {
	processStatus(PMCProcessName, pmc.messageTag, status)
}

func btof(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (pmc *PMCProcess) getMonitorSubcribeCommand() string {
	return fmt.Sprintf(
		"SET SUBSCRIBE_EVENTS_NP duration -1 "+
			"NOTIFY_PORT_STATE %s "+
			"NOTIFY_TIME_SYNC %s "+
			"NOTIFY_PARENT_DATA_SET %s "+
			"NOTIFY_CMLDS %s",
		btof(pmc.monitorPortState),
		btof(pmc.monitorTimeSync),
		btof(pmc.monitorParentData),
		btof(pmc.monitorCMLDS),
	)
}

// Start starts the PMC monitoring process.
func (pmc *PMCProcess) Start(_ context.Context) error {
	pmc.lock.Lock()
	st := pmc.processState
	switch st {
	case process.Starting, process.Running:
		pmc.lock.Unlock()
		return nil
	case process.Stopping:
		pmc.lock.Unlock()
		return fmt.Errorf("%s is stopping", pmc.Name())
	}
	pmc.stopCh = make(chan struct{})
	pmc.processState = process.Starting
	pmc.lock.Unlock()

	pmc.run()
	return nil
}

func (pmc *PMCProcess) run() {
	pmc.wg.Add(1)
	go func() {
		defer pmc.wg.Done()
		defer func() {
			if pmc.State() == process.Stopping {
				pmc.setProcessState(process.Stopped)
			} else {
				pmc.setProcessState(process.Dead)
			}
			pmc.ProcessStatus(PtpProcessDown)
			sendProcessStatusEvent(pmc.eventCh, event.PMC, pmc.configFileName, pmc.clockType, "", PtpProcessDown)
		}()
		pmc.setProcessState(process.Running)
		pmc.ProcessStatus(PtpProcessUp)
		sendProcessStatusEvent(pmc.eventCh, event.PMC, pmc.configFileName, pmc.clockType, "", PtpProcessUp)
		_ = pmc.Monitor()
	}()
}

// workerSignal represents a signal from the expectWorker to the main monitor loop
type workerSignal struct {
	err            error
	restartProcess bool
}

// Poll runs a Poll operation in a goroutine and sends the result to the struct's ParentDataSet channel
func (pmc *PMCProcess) Poll() {
	select {
	case <-pmc.stopCh:
		return
	default:
	}

	parentDS, err := pmcPkg.GetParentDS(pmc.configFileName)
	if err != nil {
		glog.Error("pmc poll failure ", err)
		return
	}

	pmc.parentDSCh <- parentDS
}

func (pmc *PMCProcess) monitor() error {
	exp, r, err := pmc.getMonitorFn(pmc.configFileName)
	if err != nil {
		if exp != nil {
			utils.CloseExpect(exp, r)
		}
		return err
	}

	doneCh := make(chan struct{})
	workerCh := make(chan workerSignal, 5)
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		pmc.expectWorker(exp, pmc.parentDSCh, workerCh, doneCh)
	}()
	defer func() {
		close(doneCh)
		utils.CloseExpect(exp, r)
		workers.Wait()
	}()

	subscribeCmd := pmc.getMonitorSubcribeCommand()
	glog.Infof("Sending '%s' to pmc", subscribeCmd)
	exp.Send(subscribeCmd + "\n")

	for {
		select {
		case <-r:
			glog.Warningf("PMC monitoring process exited")
			return fmt.Errorf("PMC needs to restart")
		case <-pmc.stopCh:
			return nil
		case parentDS := <-pmc.parentDSCh:
			go pmc.handleParentDS(parentDS)
		case signal := <-workerCh:
			if signal.restartProcess {
				glog.Warningf("PMC process exited (%v)", signal.err)
				return fmt.Errorf("PMC needs to restart")
			}
		}
	}
}

func (pmc *PMCProcess) expectWorker(exp *expect.GExpect, parentDSCh chan<- protocol.ParentDataSet, signalCh chan<- workerSignal, doneCh <-chan struct{}) {
	expectTimeout := pmc.waitInterval
	if expectTimeout <= 0 {
		expectTimeout = defaultMonitorWaitInterval
	}

	for {
		select {
		case <-pmc.stopCh:
			return
		case <-doneCh:
			return
		default:
		}

		go pmc.Poll() // Check if anything changed while handling the last message
		_, matches, expectErr := exp.Expect(pmcPkg.GetMonitorRegex(pmc.monitorParentData), expectTimeout)

		if expectErr != nil {
			if _, ok := expectErr.(expect.TimeoutError); ok {
				continue
			} else if strings.Contains(expectErr.Error(), "EOF") || strings.Contains(expectErr.Error(), "exit") {
				signalCh <- workerSignal{err: expectErr, restartProcess: true}
				return
			}
			glog.Warningf("expectWorker: unexpected error from Expect: %v", expectErr)
			continue
		}

		if len(matches) > 0 && strings.Contains(matches[0], "PARENT_DATA_SET") {
			processedMessage, procErr := protocol.ProcessMessage[protocol.ParentDataSet](matches)
			if procErr != nil {
				glog.Warningf("failed to process message for PARENT_DATA_SET: %s", procErr)
			} else {
				parentDSCh <- *processedMessage
			}
		}

	}
}

func (pmc *PMCProcess) handleParentDS(parentDS protocol.ParentDataSet) {
	if pmc.parentDS != nil && pmc.parentDS.Equal(&parentDS) {
		glog.V(14).Infof("ParentDataSet unchanged, skipping processing for %s", pmc.configFileName)
		return
	}
	glog.Info(parentDS.String())
	pmc.parentDS = &parentDS

	select {
	case pmc.eventCh <- event.Event{
		Source:    event.PMC,
		CfgName:   pmc.configFileName,
		ClockType: pmc.clockType,
		Time:      time.Now().UnixMilli(),
		Data:      &event.ParentDSData{ParentDataSet: parentDS},
	}:
	default:
		glog.Warning("event channel full, dropping ParentDS update")
	}
}

// Monitor continuously monitors the PMC process and handles restarts.
func (pmc *PMCProcess) Monitor() error {
	err := pmc.monitor()
	if err != nil {
		select {
		case <-pmc.stopCh:
			glog.Info("PMC Monitor stopping gracefully")
			return nil
		default:
			glog.Infof("pmc process hit an issue (%s)", err)
			return err
		}
	}
	return err
}

// ExitCh returns the exit channel for the process.
func (pmc *PMCProcess) ExitCh() chan struct{} {
	return pmc.stopCh
}
