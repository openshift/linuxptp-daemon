// Package ublox allows monitoring and configuring ublox data from the GPS hardware
package ublox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"

	"github.com/golang/glog"
)

const (
	// UBXCommand is the full path to the ubxtool command in the container
	UBXCommand = "/usr/local/bin/ubxtool"

	ubxtoolNew     = 0
	ubxtoolActive  = 1
	ubxtoolDead    = 2
	ubxtoolStopped = 3

	pollTimeout = "1000000000"

	// unknownOffset is outside the configured valid range and represents an
	// unavailable or malformed NAV-CLOCK accuracy value.
	unknownOffset int64 = 99999999
)

const (
	// timeLSRateSeconds is the interval in GPS navigation epochs (seconds) at which
	// the UBX-NAV-TIMELS leap-second notification is sent.
	timeLSRateSeconds = 60
)

var (
	// Disable all binary messages
	disableBinary = Command{Args: []string{"-d", "BINARY"}} // nolint:goconst
	// NAV message types to re-enable at the default rate of 1 (every second)
	navEnableMsg = []string{
		"CLOCK", "STATUS",
	}
	// NAV message types to enable at a reduced rate (timeLSRateSeconds).
	// TIMELS carries leap-second information which changes at most twice a year;
	// updating every 60 seconds is sufficient.
	navSlowEnableMsg = []string{
		"TIMELS",
	}

	// Enable all NMEA messages
	enableNMEA     = Command{Args: []string{"-e", "NMEA"}}
	nmeaDisableMsg = []string{
		"VTG", "GST", "ZDA", "GBS", "GSA", "GSV",
	}

	// All NMEA bus types to disable NMEA messages
	ublxBusTypes = []string{
		"I2C", "UART1", "UART2", "USB", "SPI",
	}

	monHW = Command{
		Args:         []string{"-w", QueryTimeout, "-p", "MON-HW"}, // nolint:goconst
		ReportOutput: true,
	}
)

// batchMsgoutAllBusses creates a command that configures messages on every bus.
func batchMsgoutAllBusses(prefix string, msgs []string, val int) Command {
	result := Command{}
	for _, msg := range msgs {
		for _, bus := range ublxBusTypes {
			result.Args = append(result.Args, "-z", fmt.Sprintf("CFG-MSGOUT-%s_%s_%s,%d", prefix, msg, bus, val))
		}
	}
	return result
}

// Generates a series of UblxCmds which disable the given message type on all bus types
// batchDisableNmeaMsgs creates commands disabling the selected NMEA messages.
func batchDisableNmeaMsgs(msgs []string) Command {
	return batchMsgoutAllBusses("NMEA_ID", msgs, 0)
}

// batchEnableNavMsgs generates commands to enable the given NAV message types
// at a rate of 1 (every GPS navigation epoch, i.e. every second) on all bus types.
// batchEnableNavMsgs creates commands enabling NAV messages at the default rate.
func batchEnableNavMsgs(msgs []string) Command {
	return batchMsgoutAllBusses("UBX_NAV", msgs, 1)
}

// batchEnableNavMsgsAtRate generates commands to enable the given NAV message types
// at the specified rate (in GPS navigation epochs) on all bus types.
// batchEnableNavMsgsAtRate creates commands enabling NAV messages at rate.
func batchEnableNavMsgsAtRate(msgs []string, rate int) Command {
	return batchMsgoutAllBusses("UBX_NAV", msgs, rate)
}

// Return the default set of commands we need to set at initialization
// defaultUblxCmds returns the baseline receiver configuration sequence.
func defaultUblxCmds() CommandList {
	// Begin by disabling all binary commands, then re-adding the ones we need
	cmds := CommandList{disableBinary}
	// Re-enable high-frequency binary commands (every second)
	cmds = append(cmds, batchEnableNavMsgs(navEnableMsg))
	// Re-enable low-frequency binary commands (every minute)
	cmds = append(cmds, batchEnableNavMsgsAtRate(navSlowEnableMsg, timeLSRateSeconds))
	// Next, enable all NMEA commands, but prune out any we don't need:
	cmds = append(cmds, enableNMEA)
	// More pruning of all bus-specific NMEA messages
	cmds = append(cmds, batchDisableNmeaMsgs(nmeaDisableMsg))

	return cmds
}

// pollProcess abstracts the running ubxtool poll process.
// Production uses pollExecProcess (wraps *exec.Cmd). Tests inject a mock.
type pollProcess interface {
	Pid() int
	Kill() error
	Wait() error
}

type pollExecProcess struct{ cmd *exec.Cmd }

func (p *pollExecProcess) Pid() int    { return p.cmd.Process.Pid }
func (p *pollExecProcess) Kill() error { return p.cmd.Process.Kill() }
func (p *pollExecProcess) Wait() error { return p.cmd.Wait() }

// startPollProcess launches the long-running ubxtool poll process and returns
// a stdout reader and a process handle. Replace in tests to inject a mock.
var startPollProcess = func(protoVersion string) (io.ReadCloser, pollProcess, error) {
	args := []string{"-u", UBXCommand, "-t", "-P", protoVersion, "-w", pollTimeout}
	cmd := exec.Command("python3", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, nil, err
	}
	return stdout, &pollExecProcess{cmd: cmd}, nil
}

// UBlox ... UBlox type
type UBlox struct {
	status       int
	statusMutex  sync.Mutex
	protoVersion string
	initResults  []string // recorded output from extra init commands with ReportOutput=true
	mockExp      func(cmdStr string) ([]string, error)
	proc         pollProcess
	reader       *bufio.Reader
	match        string

	broker       *messageBroker
	pollStopCh   chan struct{}
	pollDoneCh   chan struct{}
	pollStopMu   sync.Mutex
	pollStopOnce sync.Once
}

// InitResults returns recorded output from extra init commands that had
// ReportOutput set to true. Each entry corresponds to one command and may
// contain multiple output lines. Returns nil if no extra commands were
// provided or none had ReportOutput set.
func (u *UBlox) InitResults() []string {
	return u.initResults
}

// normalizeReportedOutput removes blank lines from command output while
// preserving the content and indentation of non-empty lines. Reported output
// is written to a status field, where empty lines add no useful information.
func normalizeReportedOutput(output string) string {
	output = strings.ReplaceAll(output, "\r\n", "\n")
	output = strings.ReplaceAll(output, "\r", "\n")

	lines := strings.Split(output, "\n")
	nonEmpty := lines[:0]
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonEmpty = append(nonEmpty, line)
		}
	}
	return strings.Join(nonEmpty, "\n")
}

// NewUblox creates and initializes a new Ublox monitoring object.
// Optional extraCmds are run after the default initialization commands
// but before SAVE (e.g., GNSS configuration from HardwareConfig).
// Returns an error if initialization fails. The returned UBlox may be non-nil
// on initialization failure and can contain recorded results from commands that
// requested output recording.
func NewUblox(extraCmds ...Command) (*UBlox, error) {
	u := UBlox{broker: newMessageBroker()}
	if err := u.Init(extraCmds...); err != nil {
		return &u, err
	}
	return &u, nil
}

// Subscribe registers a consumer for the selected UBX message types. An
// empty type list subscribes to all parsed messages. The returned channel is
// closed when the subscription is cancelled.
func (u *UBlox) Subscribe(ctx context.Context, types ...MessageType) *Subscription {
	if u.broker == nil {
		u.broker = newMessageBroker()
	}
	return u.broker.Subscribe(ctx, types...)
}

// Init detects the protocol version and sets up the core message types
// required for both GNSS monitoring and ts2phc. Optional extraCmds are run
// after the defaults but before the final SAVE.
func (u *UBlox) Init(extraCmds ...Command) error {
	runner, err := NewCommandRunner()
	if err != nil {
		return fmt.Errorf("no version detected: %w", err)
	}
	u.protoVersion = runner.protoVersion
	runner.receiver = u
	glog.Infof("UBX protocol version detected: %s", u.protoVersion)

	// Start the receiver before configuration commands so their ACK responses
	// are available through the broker.
	u.UbloxPollInit()

	// Build the full init sequence: defaults → extras → MON-HW → SAVE
	var cmds CommandList
	cmds = append(cmds, defaultUblxCmds()...)
	cmds = append(cmds, extraCmds...)
	cmds = append(cmds, monHW, SaveCommand)

	var errs []error
	for _, cmd := range cmds {
		var output string
		var runErr error
		if isPollCommand(cmd.Args) && !isAckCommand(cmd.Args) {
			responseType, responseErr := pollResponseType(cmd.Args)
			if responseErr != nil {
				runErr = responseErr
			} else {
				var response Message
				response, runErr = runner.Poll(cmd, responseType)
				if runErr == nil {
					output = strings.Join(response.Raw, "\n")
				}
			}
		} else {
			output, runErr = runner.Run(cmd)
		}
		ignoredNAK := false
		if runErr != nil {
			var nakErr *CommandNAKError
			if errors.As(runErr, &nakErr) {
				glog.Infof("ublox: ignoring command NAK during initialization: %v", nakErr)
				runErr = nil
				ignoredNAK = true
			}
		}
		errs = append(errs, runErr)
		if cmd.ReportOutput && !ignoredNAK {
			if runErr != nil {
				u.initResults = append(u.initResults, runErr.Error())
			} else {
				// Command output is exposed as a status value. Remove blank
				// lines so consumers do not render spurious whitespace.
				u.initResults = append(u.initResults, normalizeReportedOutput(output))
			}
		}
	}
	initErr := errors.Join(errs...)
	if initErr != nil {
		u.UbloxPollStop()
	}
	return initErr
}

// UbloxPollInit starts the long-running ubxtool receiver. Parsed messages
// are published to the receiver's subscriptions.
func (u *UBlox) UbloxPollInit() {
	if u.getStatus() != ubxtoolNew && u.getStatus() != ubxtoolDead {
		return
	}

	stdout, proc, err := startPollProcess(u.protoVersion)
	if err != nil {
		glog.Errorf("UbloxPoll err=%s", err.Error())
		u.setStatus(ubxtoolStopped)
		return
	}

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	u.pollStopMu.Lock()
	u.pollStopCh = stopCh
	u.pollDoneCh = doneCh
	u.pollStopOnce = sync.Once{}
	u.pollStopMu.Unlock()
	u.proc = proc
	u.reader = bufio.NewReader(stdout)
	u.setStatus(ubxtoolActive)
	glog.Infof("Starting ubxtool polling with PID=%d", proc.Pid())
	go func() {
		defer close(doneCh)
		u.ubloxPollPushThread(u.reader, stopCh)
	}()
}

// ubloxPollPushThread continually reads incoming data from ubxtool, parses
// complete UBX messages, and publishes them to all matching subscriptions.
func (u *UBlox) ubloxPollPushThread(reader *bufio.Reader, stopCh <-chan struct{}) {
	parser := newParser()
	for {
		output, err := reader.ReadString('\n')
		if !u.publishMessages(stopCh, parser.feed(output)) {
			return
		}
		if err != nil {
			if !u.publishMessages(stopCh, parser.flush()) {
				return
			}
			if u.getStatus() != ubxtoolStopped {
				u.setStatus(ubxtoolDead)
			}
			glog.Errorf("ublox poll thread error %s", err)
			return
		}
		select {
		case <-stopCh:
			return
		default:
		}
	}
}

// publishMessages publishes messages unless the reader has been cancelled.
// The cancellation check and publish are serialized with stopPollReader so a
// stop cannot race a checked-but-not-yet-published message.
func (u *UBlox) publishMessages(stopCh <-chan struct{}, messages []Message) bool {
	for _, message := range messages {
		u.pollStopMu.Lock()
		select {
		case <-stopCh:
			u.pollStopMu.Unlock()
			return false
		default:
			u.publish(message)
			u.pollStopMu.Unlock()
		}
	}
	return true
}

// publish forwards a parsed message to the receiver broker.
func (u *UBlox) publish(message Message) {
	if u.broker != nil {
		u.broker.Publish(message)
	}
}

// setStatus updates the receiver process status.
func (u *UBlox) setStatus(val int) {
	// glog.Infof("ubxtool setStatus=%d", val)
	u.statusMutex.Lock()
	u.status = val
	u.statusMutex.Unlock()
}

// getStatus returns the receiver process status.
func (u *UBlox) getStatus() int {
	u.statusMutex.Lock()
	ret := u.status
	u.statusMutex.Unlock()
	// glog.Infof("ubxtool getStatus=%d", ret)
	return ret
}

// stopPollReader signals the polling reader to stop.
func (u *UBlox) stopPollReader() <-chan struct{} {
	u.pollStopMu.Lock()
	defer u.pollStopMu.Unlock()
	if u.pollStopCh != nil {
		u.pollStopOnce.Do(func() { close(u.pollStopCh) })
	}
	return u.pollDoneCh
}

// UbloxPollReset resets the ubxtool poll process.
func (u *UBlox) UbloxPollReset() {
	if u.proc == nil {
		return
	}
	glog.Infof("Resetting ubxtool polling with PID=%d", u.proc.Pid())
	doneCh := u.stopPollReader()
	_ = u.proc.Kill()
	_ = u.proc.Wait()
	if doneCh != nil {
		<-doneCh
	}
	if u.getStatus() != ubxtoolStopped {
		u.setStatus(ubxtoolDead)
	}
}

// UbloxPollStop stops the ubxtool poll process.
func (u *UBlox) UbloxPollStop() {
	u.setStatus(ubxtoolStopped)
	if u.proc == nil {
		return
	}
	glog.Infof("Stopping ubxtool polling with PID=%d", u.proc.Pid())
	doneCh := u.stopPollReader()
	_ = u.proc.Kill()
	_ = u.proc.Wait()
	if doneCh != nil {
		<-doneCh
	}
}

// ExtractOffset extracts the tAcc offset from a single ubxtool data line.
func ExtractOffset(line string) int64 {
	if strings.Contains(line, "tAcc") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field == "tAcc" {
				if i+1 >= len(fields) {
					return -1
				}
				ret, err := strconv.ParseInt(fields[i+1], 10, 64)
				if err != nil {
					return -1
				}
				return ret
			}
		}
	}
	return -1
}

// ExtractNavStatus extracts the gpsFix state from a single ubxtool data line.
func ExtractNavStatus(line string) int64 {
	if strings.Contains(line, "gpsFix") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field == "gpsFix" {
				if i+1 >= len(fields) {
					return -1
				}
				ret, err := strconv.ParseInt(fields[i+1], 10, 64)
				if err != nil {
					return -1
				}
				return ret
			}
		}
	}
	return -1
}

// UBX-NAV-TIMELS SrcOfCurrLs / SrcOfLsChange source identifiers
const (
	LeapSourceGPS     uint8 = 2
	LeapSourceSBAS    uint8 = 3
	LeapSourceBeiDou  uint8 = 4
	LeapSourceGalileo uint8 = 5
	LeapSourceGLONASS uint8 = 6
	LeapSourceNavIC   uint8 = 7
)

// TimeLs represents GPS Leap Second data
type TimeLs struct {
	// Information source for the current number
	// of leap seconds
	SrcOfCurrLs uint8
	// Current number of leap seconds since
	// start of GPS time (Jan 6, 1980). It reflects
	// how much GPS time is ahead of UTC time.
	// Galileo number of leap seconds is the
	// same as GPS. BeiDou number of leap
	// seconds is 14 less than GPS. GLONASS
	// follows UTC time, so no leap seconds
	CurrLs int8
	// Information source for the future leap
	// second event.
	SrcOfLsChange uint8
	// Future leap second change if one is
	// scheduled. +1 = positive leap second, -1 =
	// negative leap second, 0 = no future leap
	// second event scheduled or no information
	// available. If the value is 0, then the
	// amount of leap seconds did not change
	// and the event should be ignored
	LsChange int8
	// Number of seconds until the next leap
	// second event, or from the last leap second
	// event if no future event scheduled. If > 0
	// event is in the future, = 0 event is now, < 0
	// event is in the past. Valid only if
	// validTimeToLsEvent = 1
	TimeToLsEvent int
	// GPS week number (WN) of the next leap
	// second event or the last one if no future
	// event scheduled. Valid only if
	// validTimeToLsEvent = 1.
	DateOfLsGpsWn uint
	// GPS day of week number (DN) for the next
	// leap second event or the last one if no
	// future event scheduled. Valid only if
	// validTimeToLsEvent = 1. (GPS and Galileo
	// DN: from 1 = Sun to 7 = Sat. BeiDou DN:
	// from 0 = Sun to 6 = Sat.
	DateOfLsGpsDn uint8
	// Validity flags
	// 1<<0 validCurrLs 1 = Valid current number of leap seconds value.
	// 1<<1 validTimeToLsEvent 1 = Valid time to next leap second event
	// or from the last leap second event if no future event scheduled.
	Valid uint8
}

// ExtractLeapSec extracts leap second data from the incoming ubxtool data stream
func ExtractLeapSec(output []string) *TimeLs {
	data := TimeLs{}
	for _, line := range output {
		fields := strings.Fields(line)
		for i, field := range fields {
			if i+1 >= len(fields) {
				continue
			}
			switch field {
			case "srcOfCurrLs":
				tmp, _ := strconv.ParseUint(fields[i+1], 10, 8)
				data.SrcOfCurrLs = uint8(tmp)
			case "currLs":
				tmp, _ := strconv.ParseInt(fields[i+1], 10, 8)
				data.CurrLs = int8(tmp)
			case "srcOfLsChange":
				tmp, _ := strconv.ParseUint(fields[i+1], 10, 8)
				data.SrcOfLsChange = uint8(tmp)
			case "lsChange":
				tmp, _ := strconv.ParseInt(fields[i+1], 10, 8)
				data.LsChange = int8(tmp)
			case "timeToLsEvent":
				tmp, _ := strconv.ParseInt(fields[i+1], 10, 32)
				data.TimeToLsEvent = int(tmp)
			case "dateOfLsGpsWn":
				tmp, _ := strconv.ParseUint(fields[i+1], 10, 16)
				data.DateOfLsGpsWn = uint(tmp)
			case "dateOfLsGpsDn":
				tmp, _ := strconv.ParseUint(fields[i+1], 10, 16)
				data.DateOfLsGpsDn = uint8(tmp)
			case "valid":
				tmp, _ := strconv.ParseUint(fmt.Sprintf("0%s", fields[i+1]), 0, 8)
				data.Valid = uint8(tmp)
			}
		}
	}
	return &data
}
