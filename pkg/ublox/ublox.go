// Package ublox allows monitoring and configuring ublox data from the GPS hardware
package ublox

import (
	"bufio"
	"errors"
	"fmt"
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
)

const (
	// timeLSRateSeconds is the interval in GPS navigation epochs (seconds) at which
	// the UBX-NAV-TIMELS leap-second notification is sent.
	timeLSRateSeconds = 60
)

var (
	// Disable all binary messages
	disableBinary = Command{Args: []string{"-d", "BINARY"}}
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
		Args:         []string{"-w", QueryTimeout, "-p", "MON-HW"},
		ReportOutput: true,
	}
)

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
func batchDisableNmeaMsgs(msgs []string) Command {
	return batchMsgoutAllBusses("NMEA_ID", msgs, 0)
}

// batchEnableNavMsgs generates commands to enable the given NAV message types
// at a rate of 1 (every GPS navigation epoch, i.e. every second) on all bus types.
func batchEnableNavMsgs(msgs []string) Command {
	return batchMsgoutAllBusses("UBX_NAV", msgs, 1)
}

// batchEnableNavMsgsAtRate generates commands to enable the given NAV message types
// at the specified rate (in GPS navigation epochs) on all bus types.
func batchEnableNavMsgsAtRate(msgs []string, rate int) Command {
	return batchMsgoutAllBusses("UBX_NAV", msgs, rate)
}

// Return the default set of commands we need to set at initialization
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

// UBlox ... UBlox type
type UBlox struct {
	status       int
	statusMutex  sync.Mutex
	protoVersion string
	initResults  []string // recorded output from extra init commands with ReportOutput=true
	mockExp      func(cmdStr string) ([]string, error)
	cmd          *exec.Cmd
	reader       *bufio.Reader
	match        string
	buffer       []string
	bufferlen    int
	buffermutex  sync.Mutex
}

// InitResults returns recorded output from extra init commands that had
// ReportOutput set to true. Returns nil if no extra commands were provided
// or none had ReportOutput set.
func (u *UBlox) InitResults() []string {
	return u.initResults
}

// NewUblox creates and initializes a new Ublox monitoring object.
// Optional extraCmds are run after the default initialization commands
// but before SAVE (e.g., GNSS configuration from HardwareConfig).
// Returns an error if the underlying gps channel is not available or the protocol version could not be detected.
func NewUblox(extraCmds ...Command) (*UBlox, error) {
	u := UBlox{}
	if err := u.Init(extraCmds...); err != nil {
		return nil, err
	}
	return &u, nil
}

// Init detects the protocol version and sets up the core message types
// we require for both GNSS monitoring and ts2phc. Optional extraCmds
// are run after the defaults but before the final SAVE.
func (u *UBlox) Init(extraCmds ...Command) error {
	runner, err := NewCommandRunner()
	if err != nil {
		return fmt.Errorf("no version detected: %w", err)
	}
	u.protoVersion = runner.protoVersion
	glog.Infof("UBX protocol version detected: %s", u.protoVersion)

	// Build the full init sequence: defaults → extras → MON-HW → SAVE
	var cmds CommandList
	cmds = append(cmds, defaultUblxCmds()...)
	cmds = append(cmds, extraCmds...)
	cmds = append(cmds, monHW, SaveCommand)

	var errs []error
	for _, cmd := range cmds {
		output, runErr := runner.Run(cmd)
		errs = append(errs, runErr)
		if cmd.ReportOutput {
			if runErr != nil {
				u.initResults = append(u.initResults, runErr.Error())
			} else {
				u.initResults = append(u.initResults, output)
			}
		}
	}
	return errors.Join(errs...)
}

// UbloxPollPull safely pulls data from the u.buffer
func (u *UBlox) UbloxPollPull() string {
	output := ""
	u.buffermutex.Lock()
	if u.bufferlen > 0 {
		output = u.buffer[0]
		u.buffer = u.buffer[1:]
		u.bufferlen--
	}
	u.buffermutex.Unlock()
	return output
}

// UbloxPollInit initializes the poll thread
func (u *UBlox) UbloxPollInit() {
	if u.getStatus() == ubxtoolNew || u.getStatus() == ubxtoolDead {
		u.buffermutex.Lock()
		u.bufferlen = 0
		u.buffer = nil
		u.buffermutex.Unlock()
		// Run via `python -u ubxtool` to force unbuffered stdin/stdout
		args := []string{"-u", UBXCommand, "-t", "-P", u.protoVersion, "-w", pollTimeout}
		u.cmd = exec.Command("python3", args...)
		stdoutreader, _ := u.cmd.StdoutPipe()
		u.reader = bufio.NewReader(stdoutreader)
		u.setStatus(ubxtoolActive)
		err := u.cmd.Start()
		if err != nil {
			glog.Errorf("UbloxPoll err=%s", err.Error())
			// TODO: Switching this to ubxtoolDead would allow recovery in the
			// future, but we are not making functional changes in this refactor.
			u.setStatus(ubxtoolStopped)
		} else {
			pid := u.cmd.Process.Pid
			glog.Infof("Starting ubxtool polling with PID=%d", pid)
			go u.UbloxPollPushThread()
		}
	}
}

// UbloxPollPushThread continually reads incoming data from the running ubxtool and safely appends it to u.buffer
func (u *UBlox) UbloxPollPushThread() {
	for {
		output, err := u.reader.ReadString('\n')
		if err != nil {
			if u.getStatus() != ubxtoolStopped {
				u.setStatus(ubxtoolDead)
			}
			glog.Errorf("ublox poll thread error %s", err)
			return
		} else if len(output) > 0 {
			u.buffermutex.Lock()
			u.bufferlen++
			u.buffer = append(u.buffer, output)
			u.buffermutex.Unlock()
		}
	}
}

func (u *UBlox) setStatus(val int) {
	// glog.Infof("ubxtool setStatus=%d", val)
	u.statusMutex.Lock()
	u.status = val
	u.statusMutex.Unlock()
}

func (u *UBlox) getStatus() int {
	u.statusMutex.Lock()
	ret := u.status
	u.statusMutex.Unlock()
	// glog.Infof("ubxtool getStatus=%d", ret)
	return ret
}

// UbloxPollReset resets the ubxtool poll process
func (u *UBlox) UbloxPollReset() {
	pid := u.cmd.Process.Pid
	glog.Infof("Resetting ubxtool polling with PID=%d", pid)
	_ = u.cmd.Process.Kill()
	if u.getStatus() != ubxtoolStopped {
		u.setStatus(ubxtoolDead)
	}
	u.cmd.Wait()
}

// UbloxPollStop stops the ubxtool poll process
func (u *UBlox) UbloxPollStop() {
	pid := u.cmd.Process.Pid
	glog.Infof("Stopping ubxtool polling with PID=%d", pid)
	u.setStatus(ubxtoolStopped)
	_ = u.cmd.Process.Kill()
	u.cmd.Wait()
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
