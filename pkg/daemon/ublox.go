package daemon

import (
	"bufio"
	"fmt"
	"github.com/golang/glog"
	"github.com/openshift/linuxptp-daemon/pkg/ublox"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const (
	UBX_DAEMON = "ubxtool"
)

// UBlox ... UBlox type
type UBX struct {
	protoVersion *string
	mockExp      func(cmdStr string) ([]string, error)
	name         string
	execMutex    sync.Mutex
	cmdLine      string
	cmd          *exec.Cmd
	serialPort   string
	exitCh       chan struct{}
	stopped      bool
	messageTag   string
}

// Name ... Process name
func (u *UBX) Name() string {
	return u.name
}

// ExitCh ... exit channel
func (u *UBX) ExitCh() chan struct{} {
	return u.exitCh
}

// SerialPort ... get SerialPort
func (u *UBX) SerialPort() string {
	return u.serialPort
}
func (u *UBX) setStopped(val bool) {
	u.execMutex.Lock()
	u.stopped = val
	u.execMutex.Unlock()
}

// Stopped ... check if gpspipe is stopped
func (u *UBX) Stopped() bool {
	u.execMutex.Lock()
	me := u.stopped
	u.execMutex.Unlock()
	return me
}

// CmdStop ... stop gpspipe
func (u *UBX) CmdStop() {
	glog.Infof("stopping %s...", u.name)
	if u.cmd == nil {
		return
	}
	u.setStopped(true)
	processStatus(nil, u.name, u.messageTag, PtpProcessDown)
	if u.cmd.Process != nil {
		glog.Infof("Sending TERM to (%s) PID: %d", u.name, u.cmd.Process.Pid)
		err := u.cmd.Process.Signal(syscall.SIGTERM)
		if err != nil {
			glog.Errorf("Failed to send term signal : %s", u.name)
			return
		}
	}
	// Clean up (delete) the named pipe

	glog.Infof("Process %s terminated", u.name)
}

// CmdInit ... initialize gpspipe
func (u *UBX) CmdInit() {

	//args := []string{"-t", "-p", "NAV-CLOCK", "-P", "29.20"}
	if u.name == "" {
		u.name = UBX_DAEMON
	}
	u.cmdLine = fmt.Sprintf("%s -t -p NAV-CLOCK -P 29.30 -W 1000000000", u.name)
}

// CmdRun ... run gpspipe
func (u *UBX) CmdRun(stdoutToSocket bool) {
	defer func() {
		u.exitCh <- struct{}{}
	}()
	// initiate ubx msg config
	ubxTool, _ := ublox.NewUblox()
retry:
	if ubxTool.ConfigureOffsetMsg() != nil {
		time.Sleep(2 * time.Second)
		goto retry
	}
	// start cmd to monitor
	//processStatus(nil, u.name, u.messageTag, PtpProcessUp)
	//args := strings.Split(u.cmdLine, " ")
	// u.cmd = exec.Command(args[0], args[1:]...)
	command := "ubxtool"
	args := []string{"-o", "NAV-STATUS", "-w", "1000000000"}

	for {
		u.cmd = exec.Command(command, args...)
		fmt.Println("running")
		glog.Infof("Starting %s...", u.Name())
		glog.Infof("%s cmd: %+v", u.Name(), u.cmd)

		stdout, err := u.cmd.StdoutPipe()
		if err != nil {
			glog.Errorf("CmdRun() error creating StdoutPipe for %s: %v", u.Name(), err)
			if u.Stopped() {
				return
			}
			time.Sleep(1 * time.Second)
			continue
		}

		if err := u.cmd.Start(); err != nil {
			fmt.Println("Error starting command:", err)
			return
		}
		scanner := bufio.NewScanner(stdout)
		//processStatus(nil, u.name, u.messageTag, PtpProcessUp)

		fmt.Printf("starting scanner")
		for scanner.Scan() {
			output := scanner.Text()
			fmt.Printf("%s\n", output)
		}
		fmt.Printf("nothig  to read")
		if err := u.cmd.Wait(); err != nil {
			fmt.Println("Error waiting for command:", err)
		}

		// Add a delay before retrying
		//time.Sleep(5 * time.Second)
		u.exitCh <- struct{}{}

	}
}

/*


UBX-NAV-TIMEGPS:
  iTOW 498601000 fTOW 209886 week 2287 leapS 18 valid x7 tAcc 28

UBX-NAV-CLOCK:
  iTOW 498601000 clkB -209886 clkD -686 tAcc 28 fAcc 1382

UBX-NAV-SOL:
  iTOW 498602000 fTOW 210573 week 2287 gpsFix 3 flags xdd
  ECEF X 150576965 Y -445532453 Z 429411723 pAcc 1966
  VECEF X -19 Y 9 Z 17 sAcc 90
  pDOP 318 reserved1 3 numSV 6 reserved2 119684

*/
