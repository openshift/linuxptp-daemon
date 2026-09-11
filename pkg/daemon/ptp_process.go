package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/logfilter"
	ptpnetwork "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/network"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/synce"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// ExecProcess base Process for
type ExecProcess struct {
	name       string
	configName string
	configPath string
	messageTag string
	logFilters []*logfilter.LogFilter
	eventCh    chan event.Event
	stopCh     chan struct{}
	conditions map[process.Action]process.Condition
	state      process.State
	wg         sync.WaitGroup
	execMutex  sync.RWMutex
}

// State returns the current process state of ExecProcess.
func (p *ExecProcess) State() process.State {
	p.execMutex.RLock()
	defer p.execMutex.RUnlock()
	return p.state
}

func (p *ExecProcess) setState(s process.State) {
	p.execMutex.Lock()
	p.state = s
	p.execMutex.Unlock()
}

// Name returns the name of the ExecProcess.
func (p *ExecProcess) Name() string {
	return p.name
}

// ConfigName ...
func (p *ExecProcess) ConfigName() string {
	return p.configName
}

// Conditions returns the action conditions for the ExecProcess.
func (p *ExecProcess) Conditions() map[process.Action]process.Condition {
	return p.conditions
}

// SyncInitialState ...
func (p *ExecProcess) SyncInitialState() {}

type ptpProcess struct {
	ExecProcess // TODO Make these the same

	// TODO
	cmd                 ProcessCmd
	socketPath          string
	ifaces              config.IFaces
	depProcess          []process.Process // these are list of dependent process which needs to be started/stopped if the parent process is starts/stops
	nodeProfile         *ptpv1.PtpProfile
	hasCollectedMetrics bool

	dn *Daemon
	pm *plugin.PluginManager

	clockType event.ClockType
	logParser parser.MetricsExtractor

	ptpClockThreshold     *ptpv1.PtpClockThreshold
	haProfile             map[string][]string // stores list of interface name for each profile
	syncERelations        *synce.Relations
	tBCAttributes         tBCProcessAttributes
	GrandmasterClockClass uint8
	tbcStateDetector      *hardwareconfig.PTPStateDetector // Cached PTP state detector instance
	offset                float64
	gnssSerialPort        string
}

func (p *ptpProcess) Profile() *ptpv1.PtpProfile {
	return p.nodeProfile
}

func (p *ptpProcess) ClockType() event.ClockType {
	return p.clockType
}

func (p *ptpProcess) DependentProcesses() []process.Process {
	return p.depProcess
}

// prepareTBCResources prepares cached resources for T-BC processing
// This method caches expensive operations that would otherwise be repeated 16x/second
func (p *ptpProcess) prepareTBCResources() {
	// TODO: I thinks belongs on a clock object.

	// Cache hardwareconfig availability (expensive lookup)
	if p.dn != nil {
		vTbcHasHardwareConfig = p.dn.hardwareConfigManager.HasHardwareConfigForProfile(p.nodeProfile)
	}

	// Cache PTP state detector instance (expensive creation)
	if vTbcHasHardwareConfig {
		p.tbcStateDetector = p.dn.hardwareConfigManager.GetPTPStateDetector()
	}
}

// tBCTransitionCheck performs ultra-fast T-BC transition detection (called 16x/second)
// Uses cached values and optimized processing to minimize performance impact
func (p *ptpProcess) tBCTransitionCheck(output string, pm *plugin.PluginManager) {
	// TODO: I thinks belongs on a clock object.

	// Use cached hardwareconfig availability (no expensive lookups)
	if vTbcHasHardwareConfig && p.tbcStateDetector != nil {
		// Hardwareconfig path: Use cached PTP state detector
		p.processTBCTransitionHardwareConfig(output)
	} else {
		// Legacy path: Use optimized string matching
		p.processTBCTransitionLegacy(output, pm)
	}
}

// checkOffsetFilterAndTransition checks if offset filter conditions are met and transitions to LOCKED state
// This function is called for every log line when processing the TR ports config file.
// It collects offset samples and only transitions when the filter is full and mean offset is below threshold.
// The transitionAction callback is called when conditions are met to perform the actual transition.
func (p *ptpProcess) checkOffsetFilterAndTransition(transitionAction func()) {
	// TODO: Clock object?

	if p.configName != p.tBCAttributes.trPortsConfigFile || p.tBCAttributes.offsetFilter == nil {
		return
	}

	p.tBCAttributes.offsetFilter.Insert(math.Abs(p.offset))
	if p.tBCAttributes.lastReportedState == event.PTP_LOCKED &&
		p.tBCAttributes.lastAppliedState != event.PTP_LOCKED {
		// Require filter to be full before sending event to ensure meaningful filtering
		if p.tBCAttributes.offsetFilter.IsFull() {
			tempOffset := p.tBCAttributes.offsetFilter.Mean()
			glog.Infof("Filtered Offset: %f, threshold %f", tempOffset, p.tBCAttributes.offsetThreshold)
			if tempOffset < p.tBCAttributes.offsetThreshold {
				glog.Infof("T-BC MOVE TO NORMAL STATE")
				transitionAction()
				p.tBCAttributes.lastAppliedState = event.PTP_LOCKED
			}
		}
	}
}

// sendPtp4lOffsetEvent inserts the current ptp4l offset into a sliding window and,
// once per second, sends the window average to the T-BC state machine via the event
// channel. This gives event_tbc.go visibility into ptp4l-level offsets for
// freeRunCondition and getLargestOffset calculations.
func (p *ptpProcess) sendPtp4lOffsetEvent() {
	// TODO refactor to ptp4l process

	if p.configName != p.tBCAttributes.trPortsConfigFile || p.tBCAttributes.offsetEventWindow == nil {
		return
	}
	p.tBCAttributes.offsetEventWindow.Insert(p.offset)

	nowSec := time.Now().Unix()
	if nowSec == p.tBCAttributes.lastOffsetEventSec {
		return
	}
	p.tBCAttributes.lastOffsetEventSec = nowSec

	avgOffset := int64(p.tBCAttributes.offsetEventWindow.Mean())
	glog.Infof("PTP4l offset event: %d", avgOffset)
	select {
	case p.eventCh <- event.Event{
		Source:    event.PTP4l,
		CfgName:   p.configName,
		IFace:     p.tBCAttributes.activeTRPort(),
		ClockType: p.clockType,
		Time:      time.Now().UnixMilli(),
		Data: &event.OffsetData{
			State:  p.tBCAttributes.lastReportedState,
			Offset: avgOffset,
		},
	}:
	default:
	}
}

// processTBCTransitionHardwareConfig handles T-BC transitions using hardwareconfig.
// Tracks per-port state and derives aggregate state:
//   - Any upstream port in SLAVE -> aggregate LOCKED
//   - All upstream ports lost SLAVE -> aggregate LOST (enter holdover)
//
// During a switchover (one port loses SLAVE, BMCA promotes the other), there is a
// brief gap where no port is SLAVE. Entering holdover during this gap is correct
// because the DPLL is not being disciplined by PTP. When the new port reaches SLAVE,
// the offset filter will confirm stability before exiting holdover.
func (p *ptpProcess) processTBCTransitionHardwareConfig(output string) {
	// TODO: clock objec?

	portName, conditionType := p.tbcStateDetector.DetectStateChange(output)

	switch conditionType {
	case hardwareconfig.ConditionTypeLocked:
		p.tBCAttributes.perPortState[portName] = event.PTP_LOCKED
		p.tBCAttributes.activePort = portName
		p.tBCAttributes.lastReportedState = event.PTP_LOCKED
		glog.Infof("T-BC port %s LOCKED (reported state)", portName)
		p.tBCAttributes.offsetFilter = utils.NewWindow(offsetFilterSize)

	case hardwareconfig.ConditionTypeLost:
		p.tBCAttributes.perPortState[portName] = event.PTP_FREERUN
		glog.Infof("T-BC port %s lost SLAVE", portName)

		if p.tBCAttributes.allPortsLost() {
			if err := p.dn.hardwareConfigManager.ApplyConditionForProfile(p.nodeProfile, hardwareconfig.ConditionTypeLost); err != nil {
				glog.Errorf("Failed to apply hardware config for '%s' condition: %v", hardwareconfig.ConditionTypeLost, err)
			} else {
				glog.Infof("Successfully applied hardware config for '%s' condition", hardwareconfig.ConditionTypeLost)
			}

			p.tBCAttributes.lastReportedState = event.PTP_FREERUN
			p.tBCAttributes.activePort = ""
			p.tBCAttributes.offsetFilter = nil
			glog.Info("T-BC all upstream ports lost - MOVE TO HOLDOVER")
			p.sendPtp4lStateEvent()
			p.tBCAttributes.lastAppliedState = event.PTP_HOLDOVER
		}
	}

	p.checkOffsetFilterAndTransition(func() {
		if err := p.dn.hardwareConfigManager.ApplyConditionForProfile(p.nodeProfile, hardwareconfig.ConditionTypeLocked); err != nil {
			glog.Errorf("Failed to apply hardware config for '%s' condition: %v", hardwareconfig.ConditionTypeLocked, err)
		} else {
			glog.Infof("Successfully applied hardware config for '%s' condition", hardwareconfig.ConditionTypeLocked)
		}
		p.sendPtp4lStateEvent()
		// ts2phc start is now driven by conditions, not by explicit notification
	})
}

// processTBCTransitionLegacy is the original implementation as ultimate fallback
func (p *ptpProcess) processTBCTransitionLegacy(output string, pm *plugin.PluginManager) {
	// TODO: clock object

	portMatched := false
	for _, iface := range p.tBCAttributes.trIfaceNames {
		if strings.Contains(output, iface) {
			portMatched = true
			break
		}
	}
	if portMatched {
		if strings.Contains(output, "to SLAVE on MASTER_CLOCK_SELECTED") {
			portName := parser.ExtractPortName(output)
			if portName != "" {
				if len(p.tBCAttributes.trIfaceNames) > 1 && !slices.Contains(p.tBCAttributes.trIfaceNames, portName) {
					glog.Warningf("Ignoring non-TR port in legacy MASTER_CLOCK_SELECTED event: %s", portName)
				} else {
					p.tBCAttributes.activePort = portName
					glog.Infof("T-BC active upstream port: %s", portName)
				}
			}
			p.tBCAttributes.lastReportedState = event.PTP_LOCKED
			p.tBCAttributes.offsetFilter = utils.NewWindow(offsetFilterSize)
		} else if strings.Contains(output, "to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES") ||
			strings.Contains(output, "SLAVE to") {
			pm.AfterRunPTPCommand(p.nodeProfile, "tbc-ho-entry")
			p.tBCAttributes.lastReportedState = event.PTP_FREERUN
			glog.Info("T-BC MOVE TO HOLDOVER")
			p.sendPtp4lStateEvent()
			p.tBCAttributes.lastAppliedState = event.PTP_HOLDOVER
			p.tBCAttributes.offsetFilter = nil
		}
	}

	p.checkOffsetFilterAndTransition(func() {
		pm.AfterRunPTPCommand(p.nodeProfile, "tbc-ho-exit")
		p.sendPtp4lStateEvent()
		// ts2phc start is now driven by conditions, not by explicit notification
	})
}

// cmdRun runs given ptpProcess and restarts on errors
// processOutput handles shared per-line processing for all process types:
// chronyd prefix, plugin processing, clock ID replacement, log filtering,
// metrics extraction, TBC transition check, and HA failover.
func (p *ptpProcess) processOutput(output string) string {
	if p.name == chronydProcessName {
		output = fmt.Sprintf("%s[%d]%s: %s", chronydProcessName, p.cmd.Pid(), p.messageTag, output)
	}
	output = p.pm.ProcessLog(p.name, output)
	output = p.replaceClockID(output)
	printWhenNotEmpty(logfilter.FilterOutput(p.logFilters, output))
	p.processPTPMetrics(output)
	if p.name == ptp4lProcessName {
		if p.profileClockType() == TBC {
			p.tBCTransitionCheck(output, p.pm)
		}
	} else if p.name == phc2sysProcessName && len(p.haProfile) > 0 {
		p.announceHAFailOver(output)
	}
	return output
}

// runScanner reads process stdout without delay so the pipe never backs
// up, processes each line, and pushes the result to lineCh for the
// socket-writer. It closes lineCh when the scanner finishes.
func (p *ptpProcess) runScanner(cmdReader io.Reader) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		scanner := bufio.NewScanner(cmdReader)
		for scanner.Scan() {
			p.processOutput(scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			glog.Errorf("process %s[%s] scanner exited with an errror: %s", p.name, p.configName, err)
		}
	}()
}

func (p *ptpProcess) Start(_ context.Context) error {
	p.execMutex.Lock()
	st := p.state
	switch st {
	case process.Starting, process.Running:
		p.execMutex.Unlock()
		glog.Infof("%s is already running", p.name)
		return nil
	case process.Stopping:
		p.execMutex.Unlock()
		return fmt.Errorf("%s is stopping", p.name)
	}
	p.stopCh = make(chan struct{})
	if p.cmd != nil {
		p.cmd = p.cmd.Clone()
	}
	p.state = process.Starting
	p.execMutex.Unlock()

	p.run()
	return nil
}

func (p *ptpProcess) run() {
	p.wg.Add(1)
	go func() {
		defer func() {
			// Mark run as complete then wait for all the other go routines.
			p.wg.Done()
			p.wg.Wait()

			if p.State() == process.Stopping {
				p.setState(process.Stopped)
				glog.Infof("Process %s terminated", p.name)
			} else {
				p.setState(process.Dead)
				glog.Infof("Process %s exited unexpectedly", p.name)
			}
			processStatus(p.name, p.messageTag, PtpProcessDown)
			sendProcessStatusEvent(p.eventCh, event.EventSource(p.name), p.configName, p.clockType, "", PtpProcessDown)
			p.updateGMStatusOnProcessDown(p.name)
		}()

		p.execMutex.RLock()
		cmd := p.cmd
		p.execMutex.RUnlock()
		if cmd == nil {
			glog.Errorf("CmdRun() nil cmd for %s", p.name)
			return
		}

		glog.Infof("Starting %s...", p.name)
		glog.Infof("%s cmd: %+v", p.name, cmd)

		cmdReader, err := cmd.StdoutPipe()
		if err != nil {
			glog.Errorf("CmdRun() error creating StdoutPipe for %s: %v", p.name, err)
			return
		}

		cmd.RedirectStderrToStdout()

		if err = cmd.Start(); err != nil {
			glog.Errorf("CmdRun() error starting %s: %v", p.name, err)
			return
		}
		p.execMutex.Lock()
		if p.state == process.Stopping {
			p.execMutex.Unlock()
			_ = cmd.Signal(syscall.SIGTERM)
		} else {
			p.state = process.Running
			p.execMutex.Unlock()
			processStatus(p.name, p.messageTag, PtpProcessUp)
			sendProcessStatusEvent(p.eventCh, event.EventSource(p.name), p.configName, p.clockType, "", PtpProcessUp)
		}

		p.runScanner(cmdReader)

		if waitErr := cmd.Wait(); waitErr != nil {
			glog.Errorf("CmdRun() error waiting for %s: %v", p.name, waitErr)
		}

		if p.profileClockType() == TBC && p.name == ptp4lProcessName {
			p.pm.AfterRunPTPCommand(p.nodeProfile, "reset-to-default")
		}
	}()
}

// for ts2phc along with processing metrics need to identify event
func (p *ptpProcess) processPTPMetrics(output string) {
	// TODO refactor into specific processes

	state := event.PTP_FREERUN
	if p.logParser != nil {
		processWithParser(p, output)
	} else if p.name == syncEProcessName {
		configName := strings.Replace(strings.Replace(p.messageTag, "]", "", 1), "[", "", 1)
		if configName == "" {
			return
		}
		logEntry := synce.ParseLog(output)
		p.processSynceEvents(logEntry)
	} else {
		configName, source, ptpOffset, clockState, iface := extractMetrics(p.messageTag, p.name, p.ifaces, output)
		p.hasCollectedMetrics = true
		p.offset = ptpOffset
		if iface != "" { // for ptp4l/phc2sys this function only update metrics
			var nmeaLocked *bool
			ifaceName := masterOffsetIface.getByAlias(configName, iface).name
			if iface != clockRealTime && p.name == ts2phcProcessName {
				eventSource := p.ifaces.GetEventSource(ifaceName)
				if eventSource == event.GNSS {
					nmeaLocked = event.Ptr(true)
				}
			}
			// ts2phc has to be handled differently since it announce holdover state when gnss is lost
			// TODO: verify how 1pps is handled when lost
			switch clockState {
			case FREERUN:
				state = event.PTP_FREERUN
			case LOCKED:
				state = event.PTP_LOCKED
			case HOLDOVER:
				state = event.PTP_HOLDOVER // consider s1 state as holdover,this passed to event to create metrics and events
			}
			p.processTs2PhcEvents(ptpOffset, source, ifaceName, state, nmeaLocked)
		}
	}
}

// cmdStop stops ptpProcess launched by cmdRun
func (p *ptpProcess) Stop() error {
	glog.Infof("stopping %s...", p.name)
	p.execMutex.Lock()
	st := p.state
	if st == process.Created || st == process.Stopping || st == process.Stopped || st == process.Dead {
		p.execMutex.Unlock()
		glog.Infof("%s is already stopped", p.name)
		return nil
	}
	p.state = process.Stopping
	cmd := p.cmd
	stopCh := p.stopCh
	p.execMutex.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if cmd != nil {
		glog.Infof("Sending TERM to (%s) PID: %d", p.name, cmd.Pid())
		err := cmd.Signal(syscall.SIGTERM)
		if err != nil {
			err = fmt.Errorf("failed to send SIGTERM to %s (%d): %v", p.name, cmd.Pid(), err)
			glog.Error(err)
			return err
		}
	} else {
		glog.Infof("not Sending TERM to (%s) which is nil", p.name)
	}
	return nil
}

// func (p *ptpProcess) MonitorEvent(offset float64, clockState string) {
// 	// not implemented
// }

func (p *ptpProcess) processTs2PhcEvents(ptpOffset float64, source string, iface string, state event.PTPState, nmeaLocked *bool) {
	// TODO should be ts2phc process specific
	var ptpState event.PTPState
	ptpState = state
	ptpOffsetInt64 := int64(ptpOffset)
	// if state is HOLDOVER do not update the state
	// transition to FREERUN if offset is outside configured thresholds
	if shouldFreeRun(state, ptpOffset, p.ptpClockThreshold) {
		ptpState = event.PTP_FREERUN
	}

	if source == ts2phcProcessName { // for ts2phc send it to event to create metrics and events
		select {
		case p.eventCh <- event.Event{
			Source:     event.TS2PHC,
			CfgName:    p.configName,
			IFace:      iface,
			ClockType:  p.clockType,
			Time:       time.Now().UnixMilli(),
			WriteToLog: nmeaLocked != nil,
			Reset:      false,
			Data: &event.OffsetData{
				State:      ptpState,
				Offset:     ptpOffsetInt64,
				NMEALocked: nmeaLocked,
			},
		}:
		default:
		}
	} else {
		if iface != "" && iface != clockRealTime {
			iface = alias.GetAlias(iface)
		}
		switch ptpState {
		case event.PTP_LOCKED:
			updateClockStateMetrics(p.name, iface, LOCKED, "")
		case event.PTP_FREERUN:
			updateClockStateMetrics(p.name, iface, FREERUN, "")
		case event.PTP_HOLDOVER:
			updateClockStateMetrics(p.name, iface, HOLDOVER, "")
		}
	}
}

func (p *ptpProcess) announceHAFailOver(output string) {
	// TODO refactor into phc2sys specific process

	defer func() {
		if r := recover(); r != nil {
			glog.Errorf("Recovered in f %#v", r)
		}
	}()
	var activeIFace string
	var match []string
	// selecting ens2f2 as out-of-domain source clock - 0
	// selecting ens2f0 as domain source clock - 1
	inDomain, active := failOverIndicator(output, len(p.haProfile))

	if inDomain {
		match = haInDomainRegEx.FindStringSubmatch(output)
	} else if !inDomain && active {
		match = haOutDomainRegEx.FindStringSubmatch(output)
	} else {
		return
	}

	if match != nil {
		activeIFace = match[1]
	} else {
		glog.Errorf("couldn't retrieve interface name from fail over logs %s\n", output)
		return
	}
	// find profile name and construct the log-out and metrics
	var currentProfile string
	var inActiveProfiles []string
	for profile, ifaces := range p.haProfile {
		for _, iface := range ifaces {
			if iface == activeIFace {
				currentProfile = profile
				break
			}
		}
		// mark all other profiles as inactive
		if currentProfile != profile && active {
			inActiveProfiles = append(inActiveProfiles, profile)
		}
	}
	// log both active and inactive profiles
	logString := []string{fmt.Sprintf("%s[%d]:[%s] ptp_ha_profile %s state %d\n", p.name, time.Now().Unix(), p.configName, currentProfile, boolToInt(active))}
	for _, inActive := range inActiveProfiles {
		logString = append(logString, fmt.Sprintf("%s[%d]:[%s] ptp_ha_profile %s state %d\n", p.name, time.Now().Unix(), p.configName, inActive, 0))
	}

	for _, logProfile := range logString {
		fmt.Printf("%s", logProfile)
	}
	UpdatePTPHAMetrics(currentProfile, inActiveProfiles, boolToInt(active))
}

// boolToInt converts a boolean to int64 (true=1, false=0).
func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// failOverIndicator determines domain and active state based on HA indicators.
// Returns:
//
//	inDomain: true = in-domain, false = out-of-domain
//	active: true = active profile, false = inactive profile
//
// Rules:
// - Single profile in-domain is always active (true, true)
// - Multiple profiles in-domain returns (true, false) for inactive
// - Out-of-domain active returns (false, true)
// - Default returns (false, false)
func failOverIndicator(output string, count int) (inDomain bool, active bool) {
	// TODO refactor into phc2sys specific process

	if strings.Contains(output, HAInDomainIndicator) { // in-domain indicator found
		if count == 1 {
			return true, true // single profile is always active
		}
		return true, false // multiple profiles, this one is inactive
	} else if strings.Contains(output, HAOutOfDomainIndicator) {
		return false, true // out-of-domain and active
	}
	return false, false // out-of-domain and inactive
}

// linuxptp 4.2 uses ptp device id ; this function will replace the ptp device id by the interface name
func (p *ptpProcess) replaceClockID(input string) (output string) {
	if p.name != ts2phcProcessName {
		return input
	}
	// replace only for value with offset
	if indx := strings.Index(input, offset); indx < 0 {
		return input
	}
	// Replace all occurrences of the pattern with the replacement string
	// ts2phc[1896327.319]: [ts2phc.0.config] dev/ptp4  offset    -1 s2 freq      -2
	// Find the first match
	match := clockIDRegEx.FindStringSubmatch(input)
	if match == nil {
		return input
	}
	// Extract the captured interface string (group 1)
	iface := p.ifaces.GetPhcID2IFace(match[0])
	// Fallback rationale:
	// In some cases the ts2phc log may reference a PHC device that isn't yet
	// present in this process' PHC->iface map (e.g., early logs before map build
	// or when ts2phc tracks an iface not listed in ptp4lConf). To avoid
	// mislabeling when multiple ts2phc-capable ifaces exist, we resolve the PHC
	// by scanning all PTP-capable NICs and matching their PHC IDs.
	if iface == match[0] || iface == "" {
		glog.Infof("Fallback to discover PTP devices to resolve PHC ID for %s", match[0])
		if nics, err := ptpnetwork.DiscoverPTPDevices(); err == nil {
			for _, dev := range nics {
				if ptpnetwork.GetPhcId(dev) == match[0] {
					iface = dev
					// Persist mapping so future lookups don't need fallback
					updated := false
					for idx := range p.ifaces {
						if p.ifaces[idx].Name == dev {
							p.ifaces[idx].PhcId = match[0]
							updated = true
							break
						}
					}
					if !updated {
						p.ifaces.Add(config.Iface{Name: dev, PhcId: match[0]})
					}
					break
				}
			}
		}
	}
	if iface == "" || strings.HasPrefix(iface, "/dev/ptp") {
		return input
	}

	output = clockIDRegEx.ReplaceAllString(input, iface)
	return output
}

// updateGMStatusOnProcessDown send events when  ts2phc process is down by
// send event to EventHandler
func (p *ptpProcess) updateGMStatusOnProcessDown(process string) {
	// TODO refactor into tsphc specific process

	// need to update GM status for  following process kill for  ts2phc
	if process == ts2phcProcessName {
		// ts2phc process dead should update GM-STATUS
		// Reset the entire event subsystem
		// (this nullifies the remaining pieces in the event data if ts2phc was killed during ptp profile change)
		select {
		case p.eventCh <- event.Event{
			Source:  event.TS2PHC,
			CfgName: p.configName,
			Reset:   true,
		}:
		default:
		}
	}
}

func (p *ptpProcess) processSynceEvents(logEntry synce.LogEntry) {
	// TODO refactor into SyncE process

	//                                          STATE  VALUE  DEVICE   SOURCE EXTSOURCE
	//------------------------------------------------------------------------------------
	// synce4l[627685.138]: [synce4l.0.config] LOCKED   0     synce1            GNSS
	// synce4l[627685.138]: [synce4l.0.config] LOCKED   0     synce1  ens7f0
	// synce4l[627602.593]: [synce4l.0.config] EXT_QL  255    synce1  ens7f0
	// synce4l[627602.593]: [synce4l.0.config] QL  255    synce1  ens7f0
	// synce4l[627602.593]: [synce4l.0.config] CLOCK_QUALITY  PRS    synce1  ens7f0
	// synce4l[627602.540]: [synce4l.0.config] LOCKED   0     synce1

	data := &event.SyncEData{State: event.PTP_UNKNOWN}
	clockQuality := ""
	iface := ""
	populated := false

	// synce4l[627602.540]: [synce4l.0.config] LOCKED   0     synce1
	if logEntry.State != nil && logEntry.Source != nil {
		if sDeviceConfig := p.SyncEDeviceByInterface(*logEntry.Source); sDeviceConfig != nil {
			data.Device = sDeviceConfig.Name
			data.NetworkOption = sDeviceConfig.NetworkOption
			iface = *logEntry.Source
			tState := synce.StringToEECState(strings.ReplaceAll(*logEntry.State, "EEC_LOCKED_HO_ACQ", "EEC_LOCKED"))
			glog.Infof("STATE %s", tState)
			data.State = tState.ToPTPState()
			sDeviceConfig.LastClockState = data.State
			data.EECState = *logEntry.State
			populated = true
		}
	} else if logEntry.State == nil && logEntry.Source != nil && (logEntry.QL != synce.QL_DEFAULT_SSM || logEntry.ExtQl != synce.QL_DEFAULT_SSM) {
		if sDeviceConfig := p.SyncEDeviceByInterface(*logEntry.Source); sDeviceConfig != nil {
			iface = *logEntry.Source
			// now decide on clock quality
			if sDeviceConfig.ExtendedTlv == synce.ExtendedTLV_DISABLED && logEntry.QL != synce.QL_DEFAULT_SSM {
				data.Device = sDeviceConfig.Name
				data.NetworkOption = sDeviceConfig.NetworkOption
				data.QL = event.BytePtr(logEntry.QL)
				sDeviceConfig.LastQLState[*logEntry.Source] = &synce.QualityLevelInfo{
					Priority:    0,
					SSM:         logEntry.QL,
					ExtendedSSM: synce.QL_DEFAULT_ENHSSM,
				}
				clockQuality, _ = sDeviceConfig.ClockQuality(synce.QualityLevelInfo{
					Priority:    0,
					SSM:         logEntry.QL,
					ExtendedSSM: 0,
				})
				data.State = sDeviceConfig.LastClockState
				populated = true
				UpdateSynceQLMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, "SSM", logEntry.QL)
				UpdateSynceQLMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, "Extended SSM", synce.QL_DEFAULT_ENHSSM)
				UpdateSynceClockQlMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, int(logEntry.QL)+int(synce.QL_DEFAULT_ENHSSM))
			} else if sDeviceConfig.ExtendedTlv == synce.ExtendedTLV_ENABLED {
				var lastQLState *synce.QualityLevelInfo
				var ok bool
				iface = *logEntry.Source
				if lastQLState, ok = sDeviceConfig.LastQLState[*logEntry.Source]; !ok || lastQLState == nil {
					lastQLState = &synce.QualityLevelInfo{
						Priority:    0,
						SSM:         logEntry.QL,
						ExtendedSSM: logEntry.ExtQl,
					}
					sDeviceConfig.LastQLState[*logEntry.Source] = lastQLState
				}
				if lastQLState.SSM != synce.QL_DEFAULT_SSM && logEntry.ExtQl != synce.QL_DEFAULT_SSM { // then have both ql
					data.NetworkOption = sDeviceConfig.NetworkOption
					data.Device = sDeviceConfig.Name
					data.ExtQL = event.BytePtr(logEntry.ExtQl)
					data.QL = event.BytePtr(lastQLState.SSM)
					sDeviceConfig.LastQLState[*logEntry.Source].ExtendedSSM = logEntry.ExtQl
					clockQuality, _ = sDeviceConfig.ClockQuality(synce.QualityLevelInfo{
						SSM:         lastQLState.SSM,
						ExtendedSSM: lastQLState.ExtendedSSM,
						Priority:    0,
					})

					UpdateSynceQLMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, "SSM", lastQLState.SSM)
					UpdateSynceQLMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, "Extended SSM", logEntry.ExtQl)
					UpdateSynceClockQlMetrics(syncEProcessName, p.configName, iface, sDeviceConfig.NetworkOption, sDeviceConfig.Name, int(lastQLState.SSM)+int(logEntry.ExtQl))

					data.State = sDeviceConfig.LastClockState
					populated = true
				} else if logEntry.QL != synce.QL_DEFAULT_SSM { // else we have only QL
					lastQLState.SSM = logEntry.QL // wait for extTlv
				}
			}
			if clockQuality != "" {
				data.ClockQuality = clockQuality
			}
		}
	}
	if populated {
		glog.Info(data)
		select {
		case p.eventCh <- event.Event{
			Source:     event.SYNCE,
			CfgName:    p.configName,
			IFace:      iface,
			Time:       time.Now().UnixMilli(),
			WriteToLog: true,
			Reset:      false,
			Data:       data,
		}:
		default:
		}
	}
}

func (p *ptpProcess) SyncEDeviceByInterface(iface string) *synce.Config {
	// TODO refactor into SyncE process

	if p.syncERelations != nil {
		for _, sConfig := range p.syncERelations.Devices {
			for _, name := range sConfig.Ifaces {
				if name == iface {
					return sConfig
				}
			}
		}
	}
	return nil
}

// SyncEDeviceByName ....
func (p *ptpProcess) SyncEDeviceByName(name string) *synce.Config {
	// TODO refactor into SyncE process

	if p.syncERelations != nil {
		for _, sConfig := range p.syncERelations.Devices {
			if sConfig.Name == name {
				return sConfig
			}
		}
	}
	return nil
}

func (p *ptpProcess) getPTPClockID() (string, error) {
	leadingNic, found := p.nodeProfile.PtpSettings["leadingInterface"]
	if !found {
		return "", fmt.Errorf("leadingInterface not found in ptpProfile")
	}
	key := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, leadingNic)
	leadingClockID, found := p.nodeProfile.PtpSettings[key]
	if !found {
		return "", fmt.Errorf("leading interface ClockId not found in ptpProfile")
	}
	id, err := strconv.ParseUint(leadingClockID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("failed to parse clock ID string %s: %s", leadingClockID, err)
	}
	formatKey := fmt.Sprintf("clockIdFormat[%s]", leadingNic)
	format, found := p.nodeProfile.PtpSettings[formatKey]
	if found && format == "EUI-48" {
		// MAC address format
		return fmt.Sprintf("%06x.fffe.%06x",
			id&0x0000ffffff000000>>24, id&0xffffff), nil
	}
	// Default format is EUE-64. For Intel WPC, it is EUI-64 alike, but not strictly compliant.
	// So we will fix it
	return fmt.Sprintf("%06x.fffe.%06x",
		id&0xffffff0000000000>>40, id&0xffffff), nil
}

func (p *ptpProcess) sendPtp4lStateEvent() {
	// TODO refactor into Ptp4l process

	clockID, err := p.getPTPClockID()
	if err != nil {
		glog.Error(err)
		clockID = "" // Set to empty string if error occurs
	}
	_ = clockID // Ensure linter sees the variable as used
	select {
	case p.eventCh <- event.Event{
		Source:    event.PTP4l,
		CfgName:   p.configName,
		IFace:     p.tBCAttributes.activeTRPort(),
		ClockType: p.clockType,
		Time:      time.Now().UnixMilli(),
		Reset:     false,
		Data: &event.StateData{
			State:                 p.tBCAttributes.lastReportedState,
			SourceLost:            p.tBCAttributes.lastReportedState != event.PTP_LOCKED,
			ControlledPortsConfig: p.tBCAttributes.ttPortsConfigFile,
			ClockID:               clockID,
		},
	}:
	default:
	}
}

type tBCProcessAttributes struct {
	ttPortsConfigFile string
	trPortsConfigFile string
	trIfaceNames      []string
	perPortState      map[string]event.PTPState
	activePort        string
	lastReportedState event.PTPState
	lastAppliedState  event.PTPState
	offsetFilter      *utils.Window
	offsetThreshold   float64
	// offsetEventWindow averages ptp4l offsets and sends them to the T-BC state machine once per second
	offsetEventWindow  *utils.Window
	lastOffsetEventSec int64
}

func (t *tBCProcessAttributes) activeTRPort() string {
	if t.activePort != "" {
		return t.activePort
	}
	if len(t.trIfaceNames) > 0 {
		return t.trIfaceNames[0]
	}
	return ""
}

func (t *tBCProcessAttributes) allPortsLost() bool {
	for _, state := range t.perPortState {
		if state == event.PTP_LOCKED {
			return false
		}
	}
	return true
}

func buildPtpCmdLine(name, configPath string, opts *string, nodeProfile *ptpv1.PtpProfile) string {
	o := ""
	if opts != nil {
		o = *opts
	}
	return addScheduling(nodeProfile, fmt.Sprintf("/usr/sbin/%s -f %s %s", name, configPath, o))
}

func buildCmd(cmdLine string) ProcessCmd {
	args := strings.Split(cmdLine, " ")
	return NewExecCmd(exec.Command(args[0], args[1:]...))
}

// Add fifo scheduling if specified in nodeProfile
func addScheduling(nodeProfile *ptpv1.PtpProfile, cmdLine string) string {
	if nodeProfile.PtpSchedulingPolicy != nil && *nodeProfile.PtpSchedulingPolicy == "SCHED_FIFO" {
		if nodeProfile.PtpSchedulingPriority == nil {
			glog.Errorf("Priority must be set for SCHED_FIFO; using default scheduling.")
			return cmdLine
		}
		priority := *nodeProfile.PtpSchedulingPriority
		if priority < 1 || priority > 65 {
			glog.Errorf("Invalid priority %d; using default scheduling.", priority)
			return cmdLine
		}
		cmdLine = fmt.Sprintf("/bin/chrt -f %d %s", priority, cmdLine)
		glog.Infof(cmdLine)
		return cmdLine
	}
	return cmdLine
}

type ptpProcessEnv struct {
	runID          int
	nodeProfile    *ptpv1.PtpProfile
	clockType      event.ClockType
	dn             *Daemon
	leadingNic     string
	upstreamPorts  []string
	hasFailover    bool
	osClockConfigs *OSClockConfigs
}

func profileClockType(p *ptpv1.PtpProfile) string {
	if p == nil || p.PtpSettings == nil {
		return ""
	}
	return p.PtpSettings["clockType"]
}

func (p *ptpProcess) profileClockType() string {
	return profileClockType(p.nodeProfile)
}

func optsFor(name string, profile *ptpv1.PtpProfile) *string {
	switch name {
	case ptp4lProcessName:
		if profile.Ptp4lOpts == nil {
			// ptp4l is still constructed when Opts is omitted (legacy dummy).
			s := " "
			return &s
		}
		return profile.Ptp4lOpts
	case phc2sysProcessName:
		return profile.Phc2sysOpts
	case ts2phcProcessName:
		return profile.Ts2PhcOpts
	case syncEProcessName:
		return profile.Synce4lOpts
	case chronydProcessName:
		return profile.ChronydOpts
	default:
		return nil
	}
}

func confFor(name string, profile *ptpv1.PtpProfile) *string {
	switch name {
	case ptp4lProcessName:
		return profile.Ptp4lConf
	case phc2sysProcessName:
		return profile.Phc2sysConf
	case ts2phcProcessName:
		return profile.Ts2PhcConf
	case syncEProcessName:
		return profile.Synce4lConf
	case chronydProcessName:
		return profile.ChronydConf
	default:
		return nil
	}
}

func newPtpProcess(name, configFile, configPath, socketPath, messageTag string, nodeProfile *ptpv1.PtpProfile, clockType event.ClockType, dn *Daemon) *ptpProcess {
	return &ptpProcess{
		ExecProcess: ExecProcess{
			name:       name,
			configName: configFile,
			configPath: configPath,
			messageTag: messageTag,
			logFilters: logfilter.GetLogFilters(name, messageTag, nodeProfile.PtpSettings),
			eventCh:    dn.processManager.eventsIn,
		},
		socketPath:        socketPath,
		depProcess:        []process.Process{},
		nodeProfile:       nodeProfile,
		clockType:         clockType,
		ptpClockThreshold: getPTPThreshold(nodeProfile),
		logParser:         getParser(name),
		dn:                dn,
		pm:                &dn.pluginManager,
	}
}

func (p *ptpProcess) loadProcessConf(env ptpProcessEnv) (*Ptp4lConf, *string, error) {
	confInput := confFor(p.name, env.nodeProfile)
	opts := optsFor(p.name, env.nodeProfile)

	output := &Ptp4lConf{}
	err := output.PopulatePtp4lConf(confInput, nil)
	if err != nil {
		printNodeProfile(env.nodeProfile)
		return nil, nil, err
	}
	if env.dn != nil && env.dn.interfaceResolver != nil {
		output.ResolveInterfaceNames(env.dn.interfaceResolver)
	}

	if env.nodeProfile.Interface != nil && *env.nodeProfile.Interface != "" {
		output.AddInterfaceSection(*env.nodeProfile.Interface)
	}
	return output, opts, nil
}

func (p *ptpProcess) addMonitorFlags(output *Ptp4lConf, opts *string) {
	addFlagsForMonitor(p.name, opts, output)
}

func (p *ptpProcess) writeProcessConf(output *Ptp4lConf, configOutput string) error {
	if confInput := confFor(p.name, p.nodeProfile); confInput != nil {
		*confInput = configOutput
	}
	p.gnssSerialPort = output.gnss_serial_port
	err := os.WriteFile(p.configPath, []byte(configOutput), 0o644)
	if err != nil {
		printNodeProfile(p.nodeProfile)
		return fmt.Errorf("failed to write the configuration file named %s: %v", p.configPath, err)
	}
	return nil
}

func (p *ptpProcess) renderPtp4lConf(env ptpProcessEnv, output *Ptp4lConf, opts *string) error {
	output.ExtendGlobalSection(*env.nodeProfile.Name, p.messageTag, p.socketPath, p.name)
	p.addMonitorFlags(output, opts)
	configOutput, ifaces := output.RenderPtp4lConf()
	p.ifaces = ifaces
	return p.writeProcessConf(output, configOutput)
}

func getPtp4lSocket(configPrefix string, runID int) string {
	return fmt.Sprintf("%s/ptp4l.%d.socket", configPrefix, runID)
}

func getPtp4lConfig(runID int) string {
	return fmt.Sprintf("ptp4l.%d.config", runID)
}

// NewPtp4lProcess creates a new ptp4l process instance.
func NewPtp4lProcess(env ptpProcessEnv) (*ptpProcess, error) {
	configFile := getPtp4lConfig(env.runID)
	p := newPtpProcess(
		ptp4lProcessName,
		configFile,
		fmt.Sprintf("%s/%s", configPrefix, configFile),
		getPtp4lSocket(configPrefix, env.runID),
		fmt.Sprintf("[ptp4l.%d.config:{level}]", env.runID),
		env.nodeProfile,
		env.clockType,
		env.dn,
	)
	output, opts, err := p.loadProcessConf(env)
	if err != nil {
		return nil, err
	}
	if err = p.renderPtp4lConf(env, output, opts); err != nil {
		return nil, err
	}
	for i := range p.ifaces {
		if len(env.upstreamPorts) > 0 && env.leadingNic == p.ifaces[i].Name {
			p.ifaces[i].Source = event.PTP4l
		}
	}
	p.cmd = buildCmd(buildPtpCmdLine(ptp4lProcessName, p.configPath, opts, env.nodeProfile))
	controlledConfigFile := ""
	if controlledID, ok := env.nodeProfile.PtpSettings["controlledId"]; ok {
		controlledConfigFile = fmt.Sprintf("ptp4l.%s.config", controlledID)
	}
	p.tBCAttributes = tBCProcessAttributes{
		ttPortsConfigFile: controlledConfigFile,
		trPortsConfigFile: configFile,
		lastReportedState: event.PTP_NOTSET,
		lastAppliedState:  event.PTP_NOTSET,
	}
	if len(env.upstreamPorts) > 0 && env.clockType == event.TBC {
		p.tBCAttributes.trIfaceNames = env.upstreamPorts
		p.tBCAttributes.perPortState = make(map[string]event.PTPState, len(env.upstreamPorts))
		for _, port := range env.upstreamPorts {
			p.tBCAttributes.perPortState[port] = event.PTP_NOTSET
		}
		sInSyncConditionTh, thresholdConfigured := env.nodeProfile.PtpSettings["inSyncConditionThreshold"]
		if thresholdConfigured {
			p.tBCAttributes.offsetThreshold, err = strconv.ParseFloat(sInSyncConditionTh, 64)
			if err != nil {
				return nil, fmt.Errorf("failed to parse inSyncConditionThreshold: %s", err)
			}
		} else {
			p.tBCAttributes.offsetThreshold = float64(getPTPThreshold(env.nodeProfile).MaxOffsetThreshold)
		}
		offsetEventWindowSize := defaultPtp4lOffsetEventWindowSize
		if sWindowSize, ok := env.nodeProfile.PtpSettings["ptp4lOffsetEventWindowSize"]; ok {
			if ws, parseErr := strconv.Atoi(sWindowSize); parseErr == nil && ws > 0 {
				offsetEventWindowSize = ws
			} else {
				glog.Warningf("invalid ptp4lOffsetEventWindowSize %q, using default %d", sWindowSize, defaultPtp4lOffsetEventWindowSize)
			}
		}
		p.tBCAttributes.offsetEventWindow = utils.NewWindow(offsetEventWindowSize)
		p.prepareTBCResources()
	}
	return p, nil
}

// NewPhc2sysProcess creates a new phc2sys process instance.
func NewPhc2sysProcess(env ptpProcessEnv) (*ptpProcess, error) {
	configFile := fmt.Sprintf("phc2sys.%d.config", env.runID)
	configPath := fmt.Sprintf("%s/%s", configPrefix, configFile)
	ptpHAEnabled := len(listHaProfiles(env.nodeProfile)) > 0
	var socketPath, messageTag string
	if ptpHAEnabled {
		messageTag = fmt.Sprintf("[phc2sys.%d.config:{level}]", env.runID)
	} else {
		socketPath = fmt.Sprintf("%s/ptp4l.%d.socket", configPrefix, env.runID)
		messageTag = fmt.Sprintf("[ptp4l.%d.config:{level}]", env.runID)
	}
	p := newPtpProcess(
		phc2sysProcessName,
		configFile,
		configPath,
		socketPath,
		messageTag,
		env.nodeProfile,
		env.clockType,
		env.dn,
	)
	output, opts, err := p.loadProcessConf(env)
	if err != nil {
		return nil, err
	}
	if err = p.renderPtp4lConf(env, output, opts); err != nil {
		return nil, err
	}

	if slices.Contains(env.osClockConfigs.haProfileNames, getProfileName(env.nodeProfile)) {
		*opts += " " + haSocketOpts(env.osClockConfigs)
	}

	cmdLine := buildPtpCmdLine(phc2sysProcessName, configPath, opts, env.nodeProfile)
	p.cmd = buildCmd(cmdLine)
	if p.conditions == nil {
		p.conditions = map[process.Action]process.Condition{}
	}
	startCond := phc2sysOffsetStartCondition(env)
	if env.hasFailover {
		startCond = process.Any{Conditions: []process.Condition{
			startCond,
			process.OnPluginEvent{EventName: process.GnssRecoveredEventName},
		}}
		p.conditions[process.ActionStop] = process.OnPluginEvent{EventName: process.GnssFailoverEventName}
	}
	p.conditions[process.ActionStart] = startCond
	glog.Infof("phc2sys delayed start: ActionStart=%s", startCond)
	return p, nil
}

func haPtp4lConfigs(osClockConfigs *OSClockConfigs) []string {
	configs := []string{}
	for _, profileName := range osClockConfigs.haReferencedProfiles {
		if runID, ok := osClockConfigs.profileRunID[profileName]; ok {
			configs = append(configs, getPtp4lConfig(runID))
		}
	}
	return configs
}

// TODO: Add comment explaining the conditions and why we have them
func phc2sysOffsetStartCondition(env ptpProcessEnv) process.Condition {
	base := process.OnStateAndOffsetForCount{
		State:     event.PTP_LOCKED,
		MaxOffset: 1e9,
		Count:     3,
	}
	if env.clockType == event.GM || profileClockType(env.nodeProfile) == TGM {
		base.Source = event.TS2PHC
		cfgName := fmt.Sprintf("ts2phc.%d.config", env.runID)
		base.ClockID = cfgName
		base.ConfigName = cfgName
		return base
	}
	base.Source = event.PTP4l

	cfgs := haPtp4lConfigs(env.osClockConfigs)
	if len(cfgs) == 0 {
		cfgName := fmt.Sprintf("ptp4l.%d.config", env.runID)
		base.ClockID = cfgName
		base.ConfigName = cfgName
		return base
	}
	if len(cfgs) == 1 {
		base.ClockID = cfgs[0]
		base.ConfigName = cfgs[0]
		return base
	}

	conds := make([]process.Condition, 0, len(cfgs))
	for _, cfg := range cfgs {
		conds = append(conds, process.OnStateAndOffsetForCount{
			State:      base.State,
			MaxOffset:  base.MaxOffset,
			Count:      base.Count,
			Source:     base.Source,
			ClockID:    cfg,
			ConfigName: cfg,
		})
	}
	return process.Any{Conditions: conds}
}

// shouldWaitForPhc2sys checks if ts2phc should wait for phc2sys based on profile configuration.
// Returns true if the profile has phc2sys configured or is part of an HA scenario.
func shouldWaitForPhc2sys(profile *ptpv1.PtpProfile, haProfile map[string][]string) bool {
	if profile == nil || profile.Name == nil {
		return false
	}

	// phc2sys is created if the profile has Phc2sysConf or Phc2sysOpts configured
	if profile.Phc2sysConf != nil || profile.Phc2sysOpts != nil {
		return true
	}

	// HA profiles always have phc2sys (that's the point of HA)
	// Check if this profile is part of an HA scenario
	if haProfile != nil && len(haProfile[*profile.Name]) > 0 {
		return true
	}

	return false
}

// NewTs2phcProcess creates a new ts2phc process instance.
func NewTs2phcProcess(env ptpProcessEnv) (*ptpProcess, error) {
	configFile := fmt.Sprintf("ts2phc.%d.config", env.runID)
	configPath := fmt.Sprintf("%s/%s", configPrefix, configFile)
	socketPath := fmt.Sprintf("%s/ptp4l.%d.socket", configPrefix, env.runID)
	messageTag := fmt.Sprintf("[ts2phc.%d.config:{level}]", env.runID)
	p := newPtpProcess(ts2phcProcessName, configFile, configPath, socketPath, messageTag, env.nodeProfile, env.clockType, env.dn)
	// DPLL is considered to be running along with ts2phc
	maxInSpecOffset, maxHoldoverOffSet, maxHoldoverTimeout, inSpecTimer, frequencyTraceable := dpll.CalculateTimer(env.nodeProfile)
	if env.clockType == event.GM {
		// update ts2phcOpts with the new config
		configOpts := env.nodeProfile.Ts2PhcOpts
		if configOpts != nil && *configOpts != "" {
			if !strings.Contains(*configOpts, "--ts2phc.holdover") {
				if frequencyTraceable {
					*configOpts += " --ts2phc.holdover " + strconv.FormatInt(maxHoldoverTimeout, 10)
				} else {
					*configOpts += " --ts2phc.holdover " + strconv.FormatInt(min(inSpecTimer, maxHoldoverTimeout), 10)
				}
			} // there is a 5s delay in the NMEA driver, accepting pulses 5s after the last valid NMEA message, so that might need to be subtracted from that value
			// need more testing to confirm
			if !strings.Contains(*configOpts, "--servo_offset_threshold") {
				if frequencyTraceable {
					*configOpts += " --servo_offset_threshold " + strconv.FormatInt(maxHoldoverOffSet, 10)
				} else {
					*configOpts += " --servo_offset_threshold " + strconv.FormatInt(min(maxInSpecOffset, maxHoldoverOffSet), 10)
				}
			}
			if !strings.Contains(*configOpts, "--servo_num_offset_values") { // if consecutive smaller offsets (less than the threshold) are not observed, the system stays in S2
				*configOpts += " --servo_num_offset_values 10"
			}
		}
	}
	output, opts, err := p.loadProcessConf(env)
	if err != nil {
		return nil, err
	}
	if err = p.renderPtp4lConf(env, output, opts); err != nil {
		return nil, err
	}
	p.cmd = buildCmd(buildPtpCmdLine(ts2phcProcessName, configPath, opts, env.nodeProfile))
	if profileClockType(env.nodeProfile) == TBC && env.dn != nil && p.conditions == nil {
		p.conditions = ts2phcConditionsForTBC(p, env)
	}
	return p, nil
}

// TODO: Add comment explaining the conditions and why we have them
func ts2phcConditionsForTBC(p *ptpProcess, env ptpProcessEnv) map[process.Action]process.Condition {
	conditions := map[process.Action]process.Condition{}

	// Build ts2phc start condition: ptp4l locked with stable offset, and (if configured) phc2sys locked with stable offset
	ptp4lConfigName := fmt.Sprintf("ptp4l.%d.config", env.runID)
	offsetThreshold := int64(1e9) // nanoseconds (same as current default)
	offsetSampleCount := 3        // same sample count as used currently

	startConditions := []process.Condition{
		&process.OnStateAndOffsetForCount{
			ClockID:    ptp4lConfigName,
			ConfigName: ptp4lConfigName,
			Source:     event.PTP4l,
			State:      event.PTP_LOCKED,
			MaxOffset:  float64(offsetThreshold),
			Count:      offsetSampleCount,
		},
	}

	// Add phc2sys dependency only if phc2sys is configured
	// p.haProfile is already set at this point from ApplyHaProfiles
	if shouldWaitForPhc2sys(env.nodeProfile, p.haProfile) {
		cfgName := fmt.Sprintf("ptp4l.%d.config", env.runID)
		if len(env.osClockConfigs.haProfileNames) > 0 {
			cfgName = fmt.Sprintf("phc2sys.%d.config", env.runID)
		}

		startConditions = append(startConditions, &process.OnStateAndOffsetForCount{
			ClockID:    ptp4lConfigName,
			ConfigName: cfgName,
			Source:     event.PHC2SYS,
			State:      event.PTP_LOCKED,
			MaxOffset:  float64(offsetThreshold),
			Count:      offsetSampleCount,
		})
	}

	conditions[process.ActionStart] = &process.All{Conditions: startConditions}
	return conditions
}

// NewSyncEProcess creates a new synce process instance.
func NewSyncEProcess(env ptpProcessEnv) (*ptpProcess, error) {
	configFile := fmt.Sprintf("synce4l.%d.config", env.runID)
	configPath := fmt.Sprintf("%s/%s", configPrefix, configFile)
	messageTag := fmt.Sprintf("[synce4l.%d.config]", env.runID)
	p := newPtpProcess(syncEProcessName, configFile, configPath, "", messageTag, env.nodeProfile, env.clockType, env.dn)
	output, opts, err := p.loadProcessConf(env)
	if err != nil {
		return nil, err
	}
	output.ExtendGlobalSection(*env.nodeProfile.Name, p.messageTag, p.socketPath, p.name)
	p.addMonitorFlags(output, opts)
	var configOutput string
	configOutput, p.syncERelations = output.RenderSyncE4lConf(env.nodeProfile.PtpSettings)
	if err = p.writeProcessConf(output, configOutput); err != nil {
		return nil, err
	}
	p.cmd = buildCmd(buildPtpCmdLine(syncEProcessName, configPath, opts, env.nodeProfile))
	return p, nil
}

func processStatus(processName, messageTag string, status int64) {
	cfgName := cfgNameFromMessageTag(messageTag)
	glog.V(14).Infof("processStatus: process=%s config=%s status=%d", processName, cfgName, status)
}

func cfgNameFromMessageTag(messageTag string) string {
	cfgName := strings.Replace(strings.Replace(messageTag, "]", "", 1), "[", "", 1)
	if cfgName != "" {
		cfgName = strings.Split(cfgName, MessageTagSuffixSeperator)[0]
	}
	return cfgName
}

func getPTPThreshold(nodeProfile *ptpv1.PtpProfile) *ptpv1.PtpClockThreshold {
	if nodeProfile.PtpClockThreshold != nil {
		return &ptpv1.PtpClockThreshold{
			HoldOverTimeout:    nodeProfile.PtpClockThreshold.HoldOverTimeout,
			MaxOffsetThreshold: nodeProfile.PtpClockThreshold.MaxOffsetThreshold,
		}
	} else if isNtpFailoverEnabled(nodeProfile) {
		return &ptpv1.PtpClockThreshold{
			HoldOverTimeout:    5,
			MaxOffsetThreshold: 1000,
		}
	}
	return &ptpv1.PtpClockThreshold{
		HoldOverTimeout:    5,
		MaxOffsetThreshold: 100,
	}
}

func isNtpFailoverEnabled(nodeProfile *ptpv1.PtpProfile) bool {
	pluginOpts, ok := nodeProfile.Plugins["ntpfailover"]
	if !ok || pluginOpts == nil {
		return false
	}
	var opts struct {
		GnssFailover bool `json:"gnssFailover"`
	}
	if err := json.Unmarshal(pluginOpts.Raw, &opts); err != nil {
		return false
	}
	return opts.GnssFailover
}
