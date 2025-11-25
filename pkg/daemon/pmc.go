package daemon

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	pmcPkg "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

const (
	// PMCProcessName is the name identifier for PMC processes
	PMCProcessName = "pmc"
)

// NewPMCProcess creates a new PMC process instance for monitoring PTP events.
func NewPMCProcess(runID int, eventHandler *event.EventHandler, clockType string) *PMCProcess {
	return &PMCProcess{
		configFileName:    fmt.Sprintf("ptp4l.%d.config", runID),
		messageTag:        fmt.Sprintf("[ptp4l.%d.config:{level}]", runID),
		monitorParentData: true,
		eventHandler:      eventHandler,
		clockType:         clockType,
	}
}

// PMCProcess manages a PMC (PTP Management Client) process for monitoring PTP events.
type PMCProcess struct {
	lock              sync.Mutex
	configFileName    string
	stopped           bool
	monitorPortState  bool
	monitorTimeSync   bool
	monitorParentData bool
	monitorCMLDS      bool
	parentDS          *protocol.ParentDataSet
	exitCh            chan struct{}
	clockType         string
	c                 net.Conn
	messageTag        string
	eventHandler      *event.EventHandler
}

// Name returns the process name.
func (pmc *PMCProcess) Name() string {
	return PMCProcessName
}

// Stopped returns whether the process has been stopped.
func (pmc *PMCProcess) Stopped() bool {
	pmc.lock.Lock()
	defer pmc.lock.Unlock()
	return pmc.stopped
}

func (pmc *PMCProcess) getAndSetStopped(val bool) bool {
	pmc.lock.Lock()
	defer pmc.lock.Unlock()
	oldVal := pmc.stopped
	pmc.stopped = val
	return oldVal
}

// CmdStop signals the process to stop.
func (pmc *PMCProcess) CmdStop() {
	pmc.getAndSetStopped(true)
	// Close the channel to broadcast stop signal to all receivers
	select {
	case <-pmc.exitCh:
		// Already closed
	default:
		close(pmc.exitCh)
	}
}

// CmdInit initializes the process state.
func (pmc *PMCProcess) CmdInit() {
}

// ProcessStatus processes status updates for the PMC process.
func (pmc *PMCProcess) ProcessStatus(c net.Conn, status int64) {
	if c != nil {
		pmc.c = c
	}
	processStatus(pmc.c, PMCProcessName, pmc.messageTag, status)
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

const (
	pollTimeout = 5 * time.Minute
)

// EmitClockClassLogs emits clock class change logs to the provided connection.
func (pmc *PMCProcess) EmitClockClassLogs(c net.Conn) {
	if c != nil {
		pmc.c = c
	}
	go pmc.eventHandler.EmitClockClass(pmc.configFileName, pmc.c)
}

// Poll polls the parent data set from PMC.
func (pmc *PMCProcess) Poll() error {
	parentDS, err := pmcPkg.RunPMCExpGetParentDS(pmc.configFileName)
	if err != nil {
		return err
	}
	pmc.handleParentDS(parentDS)
	return nil
}

// CmdRun starts the PMC monitoring process.
func (pmc *PMCProcess) CmdRun(stdToSocket bool) {
	isStopped := pmc.getAndSetStopped(false)
	if isStopped {
		return
	}
	pmc.exitCh = make(chan struct{}, 1)

	go func() {
		for {
			if pmc.Stopped() {
				return
			}

			var c net.Conn
			if stdToSocket {
				cAttempt, dialErr := dialSocket()
				for dialErr != nil {
					cAttempt, dialErr = dialSocket()
				}
				c = cAttempt
			}
			monitorErr := pmc.Monitor(c)
			if monitorErr == nil && pmc.Stopped() {
				// No error completed gracefully
				return
			}
		}
	}()
}

func (pmc *PMCProcess) monitor(conn net.Conn) error {
	if conn != nil {
		pmc.c = conn
	}

	err := pmc.Poll() // Set/Anounce current value to initialise or incase message was missed.
	if err != nil {
		glog.Error("Failed to initialise clock class")
	}

	exp, r, err := pmcPkg.GetPMCMontior(pmc.configFileName)
	if err != nil {
		// Clean up the spawned process if initialization failed
		if exp != nil {
			utils.CloseExpect(exp, r)
		}
		return err
	}
	defer utils.CloseExpect(exp, r)

	subscribeCmd := pmc.getMonitorSubcribeCommand()
	glog.Infof("Sending '%s' to pmc", subscribeCmd)
	exp.Send(subscribeCmd + "\n")
	for {
		_, matches, expectErr := exp.Expect(pmcPkg.GetMonitorRegex(pmc.monitorParentData), pollTimeout)
		select {
		case <-r:
			glog.Warningf("PMC monitoring process exited")
			return fmt.Errorf("PMC needs to restart")
		case <-pmc.exitCh:
			return nil // TODO close gracefully
		default:
			if expectErr != nil {
				if _, ok := expectErr.(expect.TimeoutError); ok {
					continue
				} else if strings.Contains(expectErr.Error(), "EOF") || strings.Contains(expectErr.Error(), "exit") {
					glog.Warningf("PMC process exited (%v)", expectErr)
					return fmt.Errorf("PMC needs to restart")
				}
				glog.Errorf("Error waiting for notification: %v", expectErr)
				continue
			}
			if len(matches) == 0 {
				continue
			}
			if strings.Contains(matches[0], "PARENT_DATA_SET") {
				processedMessage, procErr := protocol.ProcessMessage[protocol.ParentDataSet](matches)
				if procErr != nil {
					glog.Warningf("failed to process message for PARENT_DATA_SET: %s", procErr)
					// maybe we should attempt a poll here?
					continue
				}
				go pmc.handleParentDS(*processedMessage)
			}
		}
	}
}

func (pmc *PMCProcess) handleParentDS(parentDS protocol.ParentDataSet) {
	glog.Info(parentDS)
	oldParentDS := pmc.parentDS
	pmc.parentDS = &parentDS

	if pmc.clockType == TBC {
		data, pmcErr := pmcPkg.RunPMCExpGetTimeAndCurrentDataSets(pmc.configFileName)
		if pmcErr != nil {
			glog.Warningf("Failed to fetch TIME_PROPERTIES_DATA_SET and CURRENT_DATA_SET")
		}
		data.ParentDataSet = parentDS
		pmc.eventHandler.UpdateUpstreamData(pmc.configFileName, pmc.c, data)
	} else if oldParentDS == nil || oldParentDS.GrandmasterClockClass != parentDS.GrandmasterClockClass {
		pmc.eventHandler.AnnounceClockClass(
			fbprotocol.ClockClass(parentDS.GrandmasterClockClass),
			fbprotocol.ClockAccuracy(parentDS.GrandmasterClockClass),
			pmc.configFileName, pmc.c,
		)
	}
}

// Monitor continuously monitors the PMC process and handles restarts.
func (pmc *PMCProcess) Monitor(c net.Conn) error {
	for {
		err := pmc.monitor(c)
		if err != nil {
			// Check if we should stop before restarting
			select {
			case <-pmc.exitCh:
				glog.Info("PMC Monitor stopping gracefully")
				return nil
			default:
				// If there is an error we need to restart
				glog.Info("pmc process hit an issue (%s). restarting...", err)
				continue
			}
		}
		return err
	}
}

// ExitCh returns the exit channel for the process.
func (pmc *PMCProcess) ExitCh() chan struct{} {
	return pmc.exitCh
}

// MonitorProcess is a placeholder for process monitoring configuration.
func (pmc *PMCProcess) MonitorProcess(_ config.ProcessConfig) {
}
