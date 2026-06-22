package daemon

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
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
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
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
	execMutex            sync.Mutex
	cmdLine              string
	cmd                  *exec.Cmd
	serialPort           string
	exitCh               chan struct{}
	stopped              bool
	noFixStateOccurrence int // number of times no fix state has occurred
	offset               int64
	processConfig        config.ProcessConfig
	gmInterface          string
	messageTag           string
	ublxTool             *ublox.UBlox
	gpsdSession          *gpsdlib.Session
	gpsdDoneCh           chan bool
	sourceLost           bool
	monitorCtx           context.Context
	monitorCancel        context.CancelFunc
	c                    net.Conn
	// cmdRunner executes an external command; defaults to exec.CommandContext and
	// can be overridden in tests to inject a fake command.
	cmdRunner func(ctx context.Context, name string, args ...string) *exec.Cmd
}

// MonitorProcess ... Monitor GPSD process
func (g *GPSD) MonitorProcess(p config.ProcessConfig) {
	g.processConfig = p
}

// Name ... Process name
func (g *GPSD) Name() string {
	return g.name
}

// ExitCh ... exit channel
func (g *GPSD) ExitCh() chan struct{} {
	return g.exitCh
}

// SerialPort ... get SerialPort
func (g *GPSD) SerialPort() string {
	return g.serialPort
}
func (g *GPSD) setStopped(val bool) {
	g.execMutex.Lock()
	g.stopped = val
	g.execMutex.Unlock()
}

// Stopped ...
func (g *GPSD) Stopped() bool {
	g.execMutex.Lock()
	me := g.stopped
	g.execMutex.Unlock()
	return me
}

// CmdStop .... stop
func (g *GPSD) CmdStop() {
	glog.Infof("stopping %s...", g.name)
	// Need to ensure cleanup to workaround ublox cpu spike bug
	if g.ublxTool != nil {
		g.ublxTool.UbloxPollStop()
	}
	if g.cmd == nil {
		return
	}
	g.setStopped(true)
	g.ProcessStatus(nil, PtpProcessDown)
	if g.cmd.Process != nil {
		glog.Infof("Sending TERM to PID: %d", g.cmd.Process.Pid)
		err := g.cmd.Process.Signal(syscall.SIGTERM)
		if err != nil {
			glog.Infof("Process %s (%d) failed to terminate", g.name, g.cmd.Process.Pid)
		}
	}
	<-g.exitCh // waiting for all child routines to exit; we could add timeout to avoid waiting
	g.monitorCancel()
	glog.Infof("Process %s terminated", g.name)
}

// CmdInit ... initialize GPSD
func (g *GPSD) CmdInit() {
	if g.name == "" {
		g.name = GPSD_PROCESSNAME
	}
	g.monitorCtx, g.monitorCancel = context.WithCancel(context.Background())
	g.cmdLine = fmt.Sprintf("/usr/local/sbin/%s -p -n -S 2947 -G -N %s", g.Name(), g.SerialPort())
	if g.cmdRunner == nil {
		g.cmdRunner = exec.CommandContext
	}
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
func (g *GPSD) ProcessStatus(c net.Conn, status int64) {
	if c != nil {
		g.c = c
	}

	processStatus(g.c, g.name, g.messageTag, status)
}

// CmdRun ... run GPSD
func (g *GPSD) CmdRun(stdoutToSocket bool) {
	go g.MonitorGNSSEventsWithUblox()

	for {
		g.ProcessStatus(nil, PtpProcessUp)
		glog.Infof("Starting %s...", g.Name())
		glog.Infof("%s cmd: %+v", g.Name(), g.cmd)
		g.cmd.Stderr = &filteringStderrWriter{}
		var err error
		// Don't restart after termination
		if !g.Stopped() {
			time.Sleep(1 * time.Second)
			if resetErr := g.resetSerialPort(g.monitorCtx); resetErr != nil {
				glog.Warningf("gpsd: proceeding with start despite serial port reset failure: %v", resetErr)
			}
			err = g.cmd.Start() // this is asynchronous call,
			if err != nil {
				glog.Errorf("CmdRun() error starting %s: %v", g.Name(), err)
			}
			err = g.cmd.Wait()
			if err != nil {
				glog.Errorf("CmdRun() error waiting for %s: %v", g.Name(), err)
			}
		}
		time.Sleep(connectionRetryInterval) // Delay to prevent flooding restarts if startup fails
		// Don't restart after termination
		if g.Stopped() {
			glog.Infof("not recreating %s...", g.name)
			g.exitCh <- struct{}{} // cmdStop is waiting for confirmation
			break
		} else {
			glog.Infof("Recreating %s...", g.name)
			newCmd := exec.Command(g.cmd.Args[0], g.cmd.Args[1:]...)
			g.cmd = newCmd
		}
	}
}

// MonitorGNSSEventsWithUblox ... monitor GNSS events with ublox
func (g *GPSD) MonitorGNSSEventsWithUblox() {
	ticker := time.NewTicker(GNSSMONITOR_INTERVAL)
	doneFn := func() {
		select {
		case g.processConfig.EventChannel <- event.Event{
			Source:    event.GNSS,
			CfgName:   g.processConfig.ConfigName,
			ClockType: g.processConfig.ClockType,
			Time:      time.Now().UnixMilli(),
			Reset:     true,
		}:
		default:
			glog.Error("failed to send gnss terminated event to eventHandler")
		}
		ticker.Stop()
	}
	for {
		ublx, err := ublox.NewUblox()
		if err != nil {
			glog.Errorf("failed to initialize GNSS monitoring via ublox %s", err)
			select {
			case <-g.monitorCtx.Done():
				doneFn()
				return
			case <-time.After(GNSSMONITOR_INTERVAL):
				continue
			}
		}
		g.ublxTool = ublx
		missedTickers := 0
		for {
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
					g.processGNSSLines(strings.NewReader(strings.Join(lines, "\n")))
				}
			case <-g.monitorCtx.Done():
				doneFn()
				return
			}
		}
	}
}

// processGNSSLines reads ubxtool-formatted lines from r, extracts
// GNSS status and offset, determines the sync state, and emits an
// event on the event channel and gnssNotifyCh.
func (g *GPSD) processGNSSLines(r io.Reader) {
	const timeLsResultLines = 4
	scanner := bufio.NewScanner(r)
	nStatus := int64(0)
	nOffset := int64(99999999)
	var timeLs *ublox.TimeLs

	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "UBX-NAV-CLOCK") {
			if scanner.Scan() {
				nOffset = ublox.ExtractOffset(scanner.Text())
			}
		} else if strings.Contains(line, "UBX-NAV-STATUS") {
			if scanner.Scan() {
				nStatus = ublox.ExtractNavStatus(scanner.Text())
			}
		} else if strings.Contains(line, "UBX-NAV-TIMELS") {
			var lines []string
			for i := 0; i < timeLsResultLines; i++ {
				if !scanner.Scan() {
					break
				}
				lines = append(lines, scanner.Text())
			}
			timeLs = ublox.ExtractLeapSec(lines)
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
	if g.processConfig.EventChannel != nil {
		select {
		case g.processConfig.EventChannel <- event.Event{
			Source:     event.GNSS,
			CfgName:    g.processConfig.ConfigName,
			IFace:      g.gmInterface,
			ClockType:  g.processConfig.ClockType,
			Time:       time.Now().UnixMilli(),
			WriteToLog: true,
			Reset:      false,
			Data:       &event.GNSSData{GPSStatus: nStatus, Offset: g.offset, SourceLost: g.sourceLost},
		}:
		default:
			glog.Error("failed to send gnss event to eventHandler")
		}
	}
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
	if g.offset <= g.processConfig.GMThreshold.Max && g.offset >= g.processConfig.GMThreshold.Min {
		return true
	}
	return false
}
