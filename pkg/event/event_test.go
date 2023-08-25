package event_test

import (
	"bufio"
	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/openshift/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang/glog"
	"github.com/openshift/linuxptp-daemon/pkg/event"
)

var (
	staleSocketTimeout = 100 * time.Millisecond
)

func monkeyPatch() {

	event.PMCGMGetter = func(cfgName string) (protocol.GrandmasterSettings, error) {
		cfgName = strings.Replace(cfgName, event.TS2PHCProcessName, event.PTP4lProcessName, 1)
		return protocol.GrandmasterSettings{
			ClockQuality: fbprotocol.ClockQuality{
				ClockClass:              0,
				ClockAccuracy:           0,
				OffsetScaledLogVariance: 0,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      0,
				CurrentUtcOffsetValid: false,
				Leap59:                false,
				Leap61:                false,
				TimeTraceable:         false,
				FrequencyTraceable:    false,
				PtpTimescale:          false,
				TimeSource:            0,
			},
		}, nil
	}
	event.PMCGMSetter = func(cfgName string, g protocol.GrandmasterSettings) error {
		cfgName = strings.Replace(cfgName, event.TS2PHCProcessName, event.PTP4lProcessName, 1)
		return nil
	}
}

type PTPEvents struct {
	processName      event.EventSource
	clockState       event.PTPState
	cfgName          string
	values           map[event.ValueType]int64
	wantGMState      string // want is the expected output.
	wantClockState   string
	wantProcessState string
}

func TestEventHandler_ProcessEvents(t *testing.T) {
	monkeyPatch()
	tests := []PTPEvents{
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			values:           map[event.ValueType]int64{event.OFFSET: 0, event.PHASE_STATUS: 3, event.FREQUENCY_STATUS: 3},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 3 offset 0 phase_status 3 s2",
		},
		{
			processName:      event.GNSS,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			values:           map[event.ValueType]int64{event.OFFSET: 0, event.GPS_STATUS: 3},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "gnss[0]:[ts2phc.0.config] ens1f0 offset 0 s2",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			values:           map[event.ValueType]int64{event.OFFSET: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 offset 0 s2",
		},

		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			values:           map[event.ValueType]int64{event.OFFSET: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 offset 0 s0",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			values:           map[event.ValueType]int64{event.OFFSET: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 offset 0 s2",
		},
		{
			processName:      event.GNSS,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			values:           map[event.ValueType]int64{event.OFFSET: 0, event.GPS_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "gnss[0]:[ts2phc.0.config] ens1f0 offset 0 s0",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_HOLDOVER,
			values:           map[event.ValueType]int64{event.OFFSET: 0, event.PHASE_STATUS: 4, event.FREQUENCY_STATUS: 4},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 0 offset 0 phase_status 0 s2",
		},
	}
	logOut := make(chan string, 10)
	eChannel := make(chan event.EventChannel, 10)
	closeChn := make(chan bool)
	go listenToEvents(closeChn, logOut)
	time.Sleep(2 * time.Second)
	eventManager := event.Init("node", true, "/tmp/go.sock", eChannel, closeChn, nil, nil)
	eventManager.MockEnable()
	go eventManager.ProcessEvents()
	for _, test := range tests {
		select {
		case eChannel <- sendEvents(test.cfgName, test.processName, test.clockState, test.values):
			log.Println("sent data to channel")
			log.Println(test.cfgName, test.processName, test.clockState, test.values)
			time.Sleep(100 * time.Millisecond)
			select {
			case c := <-logOut:
				s1 := strings.Index(c, "[")
				s2 := strings.Index(c, "]")
				rs := strings.Replace(c, c[s1+1:s2], "0", -1)
				if strings.HasPrefix(c, string(test.processName)) {
					assert.Equal(t, test.wantProcessState, rs)
				}
				if strings.HasPrefix(c, "GM[") {
					assert.Equal(t, test.wantGMState, rs)
				}
				if strings.HasPrefix(c, "ptp4l[") {
					assert.Equal(t, test.wantClockState, rs)
				}
			default:
				log.Println("nothing to read")
			}

		default:
			log.Println("nothing to read")
		}
	}

	closeChn <- true
	time.Sleep(1 * time.Second)
}

func listenToEvents(closeChn chan bool, logOut chan string) {
	l, sErr := Listen("/tmp/go.sock")
	if sErr != nil {
		glog.Infof("error setting up socket %s", sErr)
		return
	}
	glog.Infof("connection established successfully")

	for {
		select {
		case <-closeChn:
			log.Println("closing socket")
			return
		default:
			fd, err := l.Accept()
			if err != nil {
				glog.Infof("accept error: %s", err)
			} else {
				ProcessTestEvents(fd, logOut)
			}
		}
	}
}

// Listen ... listen to ptp daemon logs
func Listen(addr string) (l net.Listener, e error) {
	uAddr, err := net.ResolveUnixAddr("unix", addr)
	if err != nil {
		return nil, err
	}

	// Try to listen on the socket. If that fails we check to see if it's a stale
	// socket and remove it if it is. Then we try to listen one more time.
	l, err = net.ListenUnix("unix", uAddr)
	if err != nil {
		if err = removeIfStaleUnixSocket(addr); err != nil {
			return nil, err
		}
		if l, err = net.ListenUnix("unix", uAddr); err != nil {
			return nil, err
		}
	}
	return l, err
}

// removeIfStaleUnixSocket takes in a path and removes it iff it is a socket
// that is refusing connections
func removeIfStaleUnixSocket(socketPath string) error {
	// Ensure it's a socket; if not return without an error
	if st, err := os.Stat(socketPath); err != nil || st.Mode()&os.ModeType != os.ModeSocket {
		return nil
	}
	// Try to connect
	conn, err := net.DialTimeout("unix", socketPath, staleSocketTimeout)
	if err != nil { // =syscall.ECONNREFUSED {
		return os.Remove(socketPath)
	}
	return conn.Close()
}

func ProcessTestEvents(c net.Conn, logOut chan<- string) {
	// echo received messages
	remoteAddr := c.RemoteAddr().String()
	log.Println("Client connected from", remoteAddr)
	scanner := bufio.NewScanner(c)
	for {
		ok := scanner.Scan()
		if !ok {
			break
		}
		msg := scanner.Text()
		glog.Infof("events received %s", msg)
		logOut <- msg
	}
}

func sendEvents(cfgName string, processName event.EventSource, state event.PTPState,
	values map[event.ValueType]int64) event.EventChannel {
	glog.Info("sending Nav status event to event handler Process")
	return event.EventChannel{
		ProcessName: processName,
		State:       state,
		IFace:       "ens1f0",
		CfgName:     cfgName,
		Values:      values,
		ClockType:   "GM",
		Time:        0,
		WriteToLog:  true,
		Reset:       false,
	}
}
