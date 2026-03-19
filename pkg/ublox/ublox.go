// Package ublox allows monitoring and configuring ublox data from the GPS hardware
package ublox

import (
	"bufio"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/golang/glog"
)

var regexProtoVersion = regexp.MustCompile(`PROTVER=(\d+\.\d+)`)

const (
	// UBXCommand is the full path to the ubxtool command in the container
	UBXCommand = "/usr/local/bin/ubxtool"

	UBXTOOL_NEW     = 0
	UBXTOOL_ACTIVE  = 1
	UBXTOOL_DEAD    = 2
	UBXTOOL_STOPPED = 3

	setOnlyTimeout = "0.1"
	queryTimeout   = "0.5"
	pollTimeout    = "1000000000"

	fallbackProtocolVesion = "29.20"

	cmdProtoVersion = "-p MON-VER"
)

var (
	// Disable all binary messages
	disableBinary = []string{"-d", "BINARY"}
	// Re-enable NAV-CLOCK every 1s
	enableBinaryNavClock = []string{"-p", "CFG-MSG,1,34,1"}
	// Re-enable NAV-CLOCK every 1s
	enableBinaryNavStatus = []string{"-p", "CFG-MSG,1,3,1"}

	// Enable all NMEA messages
	enableNMEA = []string{"-e", "NMEA"}
	// Disable unneeded SA messages
	disableSA = []string{"-p", "CFG-MSG,0xf0,0x02,0"}
	// Disable unneeded SV messages
	disableSv = []string{"-p", "CFG-MSG,0xf0,0x03,0"}

	// Additional NMEA messages to disable by default
	nmeaDisableMsg = []string{
		"VTG", "GST", "ZDA", "GBS",
	}

	// All NMEA bus types to disable NMEA messages
	nmeaBusTypes = []string{
		"I2C", "UART1", "UART2", "USB", "SPI",
	}

	// Final command: Save the ublox state
	saveState = []string{"-p", "SAVE"}
)

// Generates a series of UblxCmds which disable the given message type on all bus types
func cmdDisableNmeaMsg(msg string) [][]string {
	result := make([][]string, len(nmeaBusTypes))
	for i, bus := range nmeaBusTypes {
		result[i] = []string{"-z", fmt.Sprintf("CFG-MSGOUT-NMEA_ID_%s_%s,0", msg, bus)}
	}
	return result
}

// Return the default set of commands we need to set at initialization
func defaultUblxCmds() [][]string {
	// Begin by disabling all binary commands, then re-adding only NAV-CLOCK and NAV-STATUS
	cmds := [][]string{
		disableBinary, enableBinaryNavClock, enableBinaryNavStatus,
	}
	// Next, enable all NMEA commands, but prune out any we don't need:
	cmds = append(cmds,
		enableNMEA, disableSA, disableSv,
	)
	// More pruning of all bus-specific NMEA messages
	for _, msg := range nmeaDisableMsg {
		cmds = append(cmds, cmdDisableNmeaMsg(msg)...)
	}

	// Finally, save the state
	cmds = append(cmds, saveState)
	return cmds
}

// UBlox ... UBlox type
type UBlox struct {
	status       int
	statusMutex  sync.Mutex
	protoVersion string
	mockExp      func(cmdStr string) ([]string, error)
	cmd          *exec.Cmd
	reader       *bufio.Reader
	match        string
	buffer       []string
	bufferlen    int
	buffermutex  sync.Mutex
}

// NewUblox creates and initializes a new Ublox monitoring object
// Returns an error if the underlying gps channel is not available or the protocol version could not be detected
func NewUblox() (*UBlox, error) {
	u := UBlox{}
	if err := u.Init(); err != nil {
		return nil, err
	}
	return &u, nil
}

// Init detects the protocol version and sets up the core message types we require for both GNSS monitoring and ts2phc
func (u *UBlox) Init() error {
	protoVersion, err := u.query(cmdProtoVersion, regexProtoVersion)
	if err != nil {
		return fmt.Errorf("no version detected: version for method %s with error %s", "MON-VER", err)
	}
	u.protoVersion = protoVersion
	glog.Infof("UBX protocol version detected: %s", protoVersion)
	errs := []error{}
	for _, cmd := range defaultUblxCmds() {
		errs = append(errs, u.setOnly(cmd...))
	}
	return errors.Join(errs...)
}

// Execute a ubxtool query (run a command, wait for output, and match against the given regex for output)
func (u *UBlox) query(command string, promptRE *regexp.Regexp) (string, error) {
	args := []string{"-w", queryTimeout}
	if u.protoVersion == "" {
		// Only cmdProtoVersion can be run without a known protoVersion (because it establishes it)
		if command != cmdProtoVersion {
			return "", fmt.Errorf("cannot query UBlox without protocol version ")
		}
	} else {
		args = append(args, "-P", u.protoVersion)
	}
	args = append(args, strings.Fields(command)...)
	output, err := u.ubxtool(args...)
	if err != nil {
		return "", err
	}
	match := promptRE.FindStringSubmatch(output)
	if len(match) > 0 {
		return match[1], nil
	}
	return "", fmt.Errorf("ubxtool output did not match %s: %s", promptRE.String(), output)
}

// Execute a set-only command (run a command, do not wait for output)
func (u *UBlox) setOnly(command ...string) error {
	if u.protoVersion == "" {
		return fmt.Errorf("cannot set data without protocol version")
	}
	args := []string{"-w", setOnlyTimeout, "-P", u.protoVersion}
	args = append(args, command...)
	_, err := u.ubxtool(args...)
	return err
}

// Low-level command: Run ubxtool and return its output
func (u *UBlox) ubxtool(command ...string) (string, error) {
	glog.Infof("Running ubxtool %v...", command)
	output, err := exec.Command(UBXCommand, command...).CombinedOutput()
	if err != nil {
		err = fmt.Errorf("ubxtool failed: [Stdout/Stderr: %s] %w", output, err)
	}
	return string(output), err
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
	protoVersion := u.protoVersion
	if protoVersion == "" {
		// Since there's no error return, we best-effort by guessing the protocol version
		protoVersion = fallbackProtocolVesion
		glog.Warningf("Protocol version was not detected; falling back to %s", protoVersion)
	}
	if u.getStatus() == UBXTOOL_NEW || u.getStatus() == UBXTOOL_DEAD {
		u.buffermutex.Lock()
		u.bufferlen = 0
		u.buffer = nil
		u.buffermutex.Unlock()
		// Run via `python -u ubxtool` to force unbuffered stdin/stdout
		args := []string{"-u", UBXCommand, "-t", "-P", protoVersion, "-w", pollTimeout}
		u.cmd = exec.Command("python3", args...)
		stdoutreader, _ := u.cmd.StdoutPipe()
		u.reader = bufio.NewReader(stdoutreader)
		u.setStatus(UBXTOOL_ACTIVE)
		err := u.cmd.Start()
		if err != nil {
			glog.Errorf("UbloxPoll err=%s", err.Error())
			u.setStatus(UBXTOOL_STOPPED)
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
			if u.getStatus() != UBXTOOL_STOPPED {
				u.setStatus(UBXTOOL_DEAD)
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
	if u.getStatus() != UBXTOOL_STOPPED {
		u.setStatus(UBXTOOL_DEAD)
	}
	u.cmd.Wait()
}

// UbloxPollStop stops the ubxtool poll process
func (u *UBlox) UbloxPollStop() {
	pid := u.cmd.Process.Pid
	glog.Infof("Stopping ubxtool polling with PID=%d", pid)
	u.setStatus(UBXTOOL_STOPPED)
	_ = u.cmd.Process.Kill()
	u.cmd.Wait()
}

// ExtractOffset extracts the tAcc offset from the incoming ubxtool data stream
func ExtractOffset(output string) int64 {
	// Find the line that contains "tAcc"
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "tAcc") {
			// Extract the offset value
			fields := strings.Fields(line)
			for i, field := range fields {
				if field == "tAcc" {
					ret, _ := strconv.ParseInt(fields[i+1], 10, 64)
					return ret
				}
			}
		}
	}

	return -1
}

// ExtractNavStatus extracts the gpsFix state from the incoming ubxtool data stream
func ExtractNavStatus(output string) int64 {
	// Find the line that contains "gpsFix"
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if strings.Contains(line, "gpsFix") {
			// Extract the offset value
			fields := strings.Fields(line)
			for i, field := range fields {
				if field == "gpsFix" {
					ret, _ := strconv.ParseInt(fields[i+1], 10, 64)
					return ret
				}
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
