package daemon

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	gpsdlib "github.com/stratoberry/go-gpsd"
)

const (
	GPSD_PROCESSNAME     = "gpsd"
	GNSSMONITOR_INTERVAL = 1 * time.Second
)

type filteringStderrWriter struct{}

func (w *filteringStderrWriter) Write(p []byte) (n int, err error) {
	if bytes.Contains(p, []byte("Inappropriate ioctl for device")) {
		// Suppress this error
		return len(p), nil
	}
	// Write all other output to the real stderr (container logs)
	return os.Stderr.Write(p)
}

type GPSD struct {
	name                 string
	execMutex            sync.RWMutex
	cmdLine              string
	cmd                  ProcessCmd
	serialPort           string
	stopCh               chan struct{}
	stopped              bool
	noFixStateOccurrence int // number of times no fix state has occurred
	offset               int64
	processConfig        config.ProcessConfig
	gmInterface          string
	messageTag           string
	ublxTool             *ublox.UBlox
	gnssInitCmds         ublox.CommandList      // optional HardwareConfig GNSS init commands
	gnssResultsFn        func(results []string) // callback to store GNSS init results
	gpsdSession          *gpsdlib.Session
	gpsdDoneCh           chan bool
	sourceLost           bool
	monitorCtx           context.Context
	monitorCancel        context.CancelFunc
	eventCh              chan event.Event
	// cmdRunner executes an external command; defaults to exec.CommandContext and
	// can be overridden in tests to inject a fake command.
	cmdRunner  func(ctx context.Context, name string, args ...string) *exec.Cmd
	state      process.State
	profile    *ptpv1.PtpProfile
	conditions map[process.Action]process.Condition
	wg         sync.WaitGroup
}

// NewGpsdProcess creates a new GPSD process instance.
func NewGpsdProcess(serialPort string, gmInterface string, messageTag string, gnssInitCmds ublox.CommandList, gnssResultsFn func(results []string), nodeProfile *ptpv1.PtpProfile, eventCh chan event.Event, processCfg config.ProcessConfig) process.Process {
	monitorCtx, monitorCancel := context.WithCancel(context.Background())

	cmdLine := fmt.Sprintf("/usr/local/sbin/%s -p -n -S 2947 -G -N %s", GPSD_PROCESSNAME, serialPort)
	cmdLine = addScheduling(nodeProfile, cmdLine)
	args := strings.Split(cmdLine, " ")

	if processCfg.EventChannel == nil && eventCh != nil {
		processCfg.EventChannel = eventCh
	}

	return &GPSD{
		name:          GPSD_PROCESSNAME,
		cmdLine:       cmdLine,
		cmd:           NewExecCmd(exec.Command(args[0], args[1:]...)),
		serialPort:    serialPort,
		gmInterface:   gmInterface,
		stopped:       false,
		messageTag:    messageTag,
		ublxTool:      nil,
		gnssInitCmds:  gnssInitCmds,
		gnssResultsFn: gnssResultsFn,
		cmdRunner:     exec.CommandContext,
		monitorCtx:    monitorCtx,
		monitorCancel: monitorCancel,
		eventCh:       eventCh,
		processConfig: processCfg,
		profile:       nodeProfile,
		conditions: map[process.Action]process.Condition{
			process.ActionStart: process.OnProcessUp{
				Source:     event.GPSPIPE,
				ConfigName: processCfg.ConfigName,
			},
		},
	}
}

// Name ... Process name
func (g *GPSD) Name() string {
	return g.name
}

// Conditions returns the action conditions for the GPSD process.
func (g *GPSD) Conditions() map[process.Action]process.Condition {
	return g.conditions
}

// Profile returns the PTP profile associated with the GPSD process.
func (g *GPSD) Profile() *ptpv1.PtpProfile {
	return g.profile
}

// ConfigName ...
func (g *GPSD) ConfigName() string {
	return g.processConfig.ConfigName
}

// SyncInitialState ...
func (g *GPSD) SyncInitialState() {}

func (g *GPSD) gnssClockType() event.ClockType {
	if g.processConfig.ClockType != "" {
		return g.processConfig.ClockType
	}
	return event.GM
}

func (g *GPSD) sendGNSSEvent(ev event.Event) {
	select {
	case g.eventCh <- ev:
	default:
		glog.Error("failed to send gnss event to ProcessManager")
	}
}

// ClockType returns the clock type for GPSD (always GM).
func (g *GPSD) ClockType() event.ClockType {
	return event.GM
}

// DependentProcesses returns processes that depend on GPSD.
func (g *GPSD) DependentProcesses() []process.Process {
	return []process.Process{}
}

// State returns the current process state of GPSD.
func (g *GPSD) State() process.State {
	g.execMutex.RLock()
	defer g.execMutex.RUnlock()
	return g.state
}

func (g *GPSD) setState(s process.State) {
	g.execMutex.Lock()
	g.state = s
	g.execMutex.Unlock()
}

// CmdStop .... stop
func (g *GPSD) Stop() error {
	glog.Infof("stopping %s...", g.name)
	g.execMutex.Lock()
	st := g.state
	if st == process.Created || st == process.Stopping || st == process.Stopped || st == process.Dead {
		g.execMutex.Unlock()
		glog.Infof("%s is already stopped", g.name)
		return nil
	}
	g.state = process.Stopping
	cmd := g.cmd
	stopCh := g.stopCh
	ublx := g.ublxTool
	cancel := g.monitorCancel
	g.execMutex.Unlock()

	if ublx != nil {
		ublx.UbloxPollStop()
	}
	if stopCh != nil {
		close(stopCh)
	}
	if cancel != nil {
		cancel()
	}
	if cmd != nil {
		glog.Infof("Sending TERM to PID: %d", cmd.Pid())
		err := cmd.Signal(syscall.SIGTERM)
		if err != nil {
			err = fmt.Errorf("process %s (%d) failed to terminate", g.name, cmd.Pid())
			glog.Info(err.Error())
			return err
		}
	}
	glog.Info("GPSD: waiting for all child routines to exit")
	g.wg.Wait()
	return nil
}

// resetSerialPort resets the serial device to a sane state before starting gpsd.
// On platforms where GNSS is connected via UART (e.g. HPE), an abrupt gpsd
// termination can leave the UART in a dirty state that prevents a new gpsd
// instance from communicating with the GNSS module. Running "stty sane" clears
// that state. The reset is unconditional — it is also harmless on platforms
// with USB-connected GNSS devices.
// If serialPort is empty the reset is skipped with a warning.
// A reset failure is logged as a warning but never blocks gpsd startup.
func (g *GPSD) resetSerialPort(ctx context.Context) error {
	if g.serialPort == "" {
		glog.Warningf("gpsd: no serial port configured, skipping device reset before gpsd start")
		return nil
	}
	out, err := g.cmdRunner(ctx, "stty", "-F", g.serialPort, "sane").CombinedOutput()
	if err != nil {
		return fmt.Errorf("stty -F %s sane: %w (output: %s)", g.serialPort, err, strings.TrimSpace(string(out)))
	}
	glog.Infof("gpsd: serial port %s reset to sane state", g.serialPort)
	return nil
}

// ProcessStatus ...
func (g *GPSD) ProcessStatus(status int64) {
	processStatus(g.name, g.messageTag, status)
}

func (g *GPSD) run() {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer func() {
			if g.State() == process.Stopping {
				g.setState(process.Stopped)
				glog.Infof("Process %s terminated", g.name)
			} else {
				g.setState(process.Dead)
				glog.Infof("Process %s exited unexpectedly", g.name)
			}
			g.ProcessStatus(PtpProcessDown)
			sendProcessStatusEvent(g.eventCh, event.GPSD, cfgNameFromMessageTag(g.messageTag), g.ClockType(), g.gmInterface, PtpProcessDown)
		}()

		g.execMutex.RLock()
		cmd := g.cmd
		g.execMutex.RUnlock()
		if cmd == nil {
			glog.Errorf("run() nil cmd for %s", g.Name())
			return
		}

		glog.Infof("Starting %s...", g.Name())
		glog.Infof("%s cmd: %+v", g.Name(), cmd)
		cmd.SetStderr(&filteringStderrWriter{})
		if resetErr := g.resetSerialPort(g.monitorCtx); resetErr != nil {
			glog.Warningf("gpsd: proceeding with start despite serial port reset failure: %v", resetErr)
		}
		err := cmd.Start()
		if err != nil {
			glog.Errorf("run() error starting %s: %v", g.Name(), err)
			return
		}
		g.execMutex.Lock()
		if g.state == process.Stopping {
			g.execMutex.Unlock()
			_ = cmd.Signal(syscall.SIGTERM)
		} else {
			g.state = process.Running
			g.execMutex.Unlock()
			g.ProcessStatus(PtpProcessUp)
			sendProcessStatusEvent(g.eventCh, event.GPSD, cfgNameFromMessageTag(g.messageTag), g.ClockType(), g.gmInterface, PtpProcessUp)
		}

		err = cmd.Wait()
		if err != nil {
			glog.Errorf("run() error waiting for %s: %v", g.Name(), err)
		}
	}()
}

// Start starts the GPSD process.
func (g *GPSD) Start(_ context.Context) error {
	g.execMutex.Lock()
	st := g.state
	switch st {
	case process.Starting, process.Running:
		g.execMutex.Unlock()
		glog.Infof("%s is already running", g.name)
		return nil
	case process.Stopping:
		g.execMutex.Unlock()
		return fmt.Errorf("%s is stopping", g.name)
	}
	g.stopCh = make(chan struct{})
	if g.cmd != nil {
		g.cmd = g.cmd.Clone()
	}
	startGNSS := st == process.Created || st == process.Stopped
	if st == process.Stopped {
		g.monitorCtx, g.monitorCancel = context.WithCancel(context.Background())
	}
	g.state = process.Starting
	g.execMutex.Unlock()

	g.run()
	if startGNSS {
		g.monitorGNSSEventsWithUblox()
	}
	return nil
}

// MonitorGNSSEventsWithUblox ... monitor GNSS events with ublox
func (g *GPSD) monitorGNSSEventsWithUblox() {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		ticker := time.NewTicker(GNSSMONITOR_INTERVAL)
		defer func() {
			g.sendGNSSEvent(event.Event{
				Source:    event.GNSS,
				CfgName:   g.ConfigName(),
				ClockType: g.gnssClockType(),
				Time:      time.Now().UnixMilli(),
				Reset:     true,
			})
			ticker.Stop()
		}()
		for {
			ublx, err := ublox.NewUblox(g.gnssInitCmds...)
			if err != nil {
				glog.Errorf("failed to initialize GNSS monitoring via ublox %s", err)
				g.execMutex.RLock()
				stopCh := g.stopCh
				g.execMutex.RUnlock()
				select {
				case <-g.monitorCtx.Done():
					return
				case <-stopCh:
					return
				case <-time.After(GNSSMONITOR_INTERVAL):
					continue
				}
			}
			g.ublxTool = ublx
			if results := ublx.InitResults(); len(results) > 0 && g.gnssResultsFn != nil {
				g.gnssResultsFn(results)
			}
			missedTickers := 0
			for {
				g.execMutex.RLock()
				stopCh := g.stopCh
				g.execMutex.RUnlock()
				select {
				case <-ticker.C:
					ublx.UbloxPollInit()
					var lines []string
					emptyCount := 0
					for {
						line := ublx.UbloxPollPull()
						if len(line) == 0 {
							emptyCount++
							if emptyCount >= 10 {
								missedTickers++
								if missedTickers > 3 {
									ublx.UbloxPollReset()
									missedTickers = 0
								}
								break
							}
							continue
						}
						emptyCount = 0
						missedTickers = 0
						lines = append(lines, line)
					}
					if len(lines) > 0 {
						g.processGNSSLines(lines)
					}
				case <-g.monitorCtx.Done():
					return
				case <-stopCh:
					return
				}
			}
		}
	}()
}

// processGNSSLines parses ubxtool-formatted lines, extracts GNSS status and
// offset, determines the sync state, and emits an event on the event channel.
// Each element of lines is one ubxtool output line (trailing newline optional).
func (g *GPSD) processGNSSLines(lines []string) {
	const timeLsResultLines = 4
	nStatus := int64(0)
	nOffset := int64(99999999)
	var timeLs *ublox.TimeLs

	for i, line := range lines {
		if strings.Contains(line, "UBX-NAV-CLOCK") {
			if i+1 < len(lines) {
				nOffset = ublox.ExtractOffset(lines[i+1])
			}
		} else if strings.Contains(line, "UBX-NAV-STATUS") {
			if i+1 < len(lines) {
				nStatus = ublox.ExtractNavStatus(lines[i+1])
			}
		} else if strings.Contains(line, "UBX-NAV-TIMELS") {
			end := i + 1 + timeLsResultLines
			if end > len(lines) {
				end = len(lines)
			}
			timeLs = ublox.ExtractLeapSec(lines[i+1 : end])
		}
	}

	g.offset = nOffset
	g.sourceLost = false
	switch nStatus >= 3 {
	case true:
		if !g.isOffsetInRange() {
			g.sourceLost = true
		}
	default:
		g.sourceLost = true
	}
	g.sendGNSSEvent(event.Event{
		Source:     event.GNSS,
		CfgName:    g.ConfigName(),
		IFace:      g.gmInterface,
		ClockType:  g.gnssClockType(),
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Reset:      false,
		Data:       &event.GNSSData{GPSStatus: nStatus, Offset: g.offset, SourceLost: g.sourceLost},
	})
	if timeLs != nil && leap.LeapMgr != nil {
		select {
		case leap.LeapMgr.UbloxLsInd <- *timeLs:
		case <-time.After(100 * time.Millisecond):
			glog.Infof("failed to send leap event updates")
		}
	}
}

// isOffsetInRange ... check if offset is in range
func (g *GPSD) isOffsetInRange() bool {
	return math.Abs(float64(g.offset)) < float64(g.processConfig.GMThreshold.Max)
}
