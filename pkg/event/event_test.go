package event_test

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clockmgr"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fbprotocol "github.com/facebook/time/ptp/protocol"
)

func TestMain(m *testing.M) {
	teardownTests := testhelpers.SetupTests()
	defer teardownTests()
	os.Exit(m.Run())
}

const (
	testBCCfgPTP4l  = "ptp4l.0.config"
	testBCCfgTS2PHC = "ts2phc.0.config"
	testClockID     = "001122.fffe.334455"
	testHelpStr     = "test"
	testFromLabel   = "from"
	testIfaceLabel  = "iface"
)

var (
	staleSocketTimeout = 100 * time.Millisecond
)

func newTestPMCMock() *pmc.MockClient {
	return &pmc.MockClient{
		GMSettingsResult: protocol.GrandmasterSettings{
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
				TimeTraceable:         true,
				FrequencyTraceable:    true,
				PtpTimescale:          false,
				TimeSource:            0,
			},
		},
	}
}

type PTPEvents struct {
	processName      event.EventSource
	clockState       event.PTPState
	cfgName          string
	iface            string
	outOfSpec        bool
	values           map[event.ValueType]interface{}
	wantGMState      string // want is the expected output.
	wantClockState   string
	wantProcessState string
	desc             string
	sourceLost       bool
}

func TestEventHandler_ProcessEvents(t *testing.T) {
	pmcMock := newTestPMCMock()
	tests := []PTPEvents{
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 3, event.FREQUENCY_STATUS: 3, event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] unknown T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 3 offset 0 phase_status 3 pps_status 1 s2",
			desc:             "Initial state, gnss is not set, so expect GM to be FREERUN",
		},
		{
			processName:      event.GNSS,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.GPS_STATUS: 3},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "gnss[0]:[ts2phc.0.config] ens1f0 gnss_status 3 offset 0 s2",
			desc:             "gnss is locked and has dpll in locked state,but ts2phc has not yet reported so GM will be FREERUN ",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0)},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 offset 0 s2",
			desc:             "ts2phc is now reported as locked, GM should be in locked state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(5000)},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 offset 5000 s0",
			desc:             "ts2phc is reporting FREERUN, GM should be in FREERUN state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0)},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 offset 0 s2",
			desc:             "ts2phc is also reporting locked, GM should be in locked state",
		},
		{
			processName:      event.GNSS,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.GPS_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "gnss[0]:[ts2phc.0.config] ens1f0 gnss_status 0 offset 0 s0",
			sourceLost:       true,
			desc:             "GPS is free run ,source is lost when everything else is locked(Do nothing and wait  for DPLL to switch to HOLDOVER)",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_HOLDOVER,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 4, event.FREQUENCY_STATUS: 4, event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s1",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 7",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 4 offset 0 phase_status 4 pps_status 1 s1",
			desc:             "dpll is on Holdover, where source is lost, move to holdover state",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        true,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 1, event.FREQUENCY_STATUS: 1, event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 1 offset 0 phase_status 1 pps_status 1 s0",
			desc:             "dpll move to FREERUN from holdover (out of spec)",
		},
		{
			processName:      event.GNSS,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.GPS_STATUS: 3},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 140",
			wantProcessState: "gnss[0]:[ts2phc.0.config] ens1f0 gnss_status 3 offset 0 s2",
			sourceLost:       false,
			desc:             "GPS is locked but dpll is in FREERUN and out of spec, yet to switch over in that case GM should stay with last state",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			outOfSpec:        true,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 3, event.FREQUENCY_STATUS: 3, event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 3 offset 0 phase_status 3 pps_status 1 s2",
			desc:             "everything is in locked state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_HOLDOVER,
			outOfSpec:        true,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(99999), event.NMEA_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s1",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 7",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 nmea_status 0 offset 99999 s1",
			desc:             "ts2phc is not in locked state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        true,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(99999), event.NMEA_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 nmea_status 0 offset 99999 s0",
			desc:             "ts2phc is not in locked state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			outOfSpec:        true,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.NMEA_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens1f0 nmea_status 1 offset 0 s2",
			desc:             "everything is in locked state",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        true,
			iface:            "ens2f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(5000), event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s0",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 248",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens2f0 offset 5000 pps_status 1 s0",
			desc:             "2nd card ts2phc offset spiked",
		},
		{
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_LOCKED,
			outOfSpec:        true,
			iface:            "ens2f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.NMEA_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens2f0 nmea_status 1 offset 0 s2",
			desc:             "2nd card restored ",
		},
		{ // add scenario where first GNSS is lost and then DPLL 1 and 2 both  is switching to HOLDOVER
			processName:      event.GNSS,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.GPS_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s2",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 6",
			wantProcessState: "gnss[0]:[ts2phc.0.config] ens1f0 gnss_status 0 offset 0 s0",
			sourceLost:       true,
			desc:             "Case 2: GPS is free run ,source is lost when everything else is locked(Do nothing and wait  for DPLL to switch to HOLDOVER)",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_HOLDOVER,
			outOfSpec:        false,
			iface:            "ens1f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 4, event.FREQUENCY_STATUS: 4, event.PPS_STATUS: 1},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s1",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 7",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens1f0 frequency_status 4 offset 0 phase_status 4 pps_status 1 s1",
			desc:             "dpll is on Holdover, where source is lost, moving to holdover state",
		},
		{
			processName:      event.DPLL,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_HOLDOVER,
			outOfSpec:        false,
			iface:            "ens2f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(0), event.PHASE_STATUS: 4, event.FREQUENCY_STATUS: 4, event.PPS_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s1",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 7",
			wantProcessState: "dpll[0]:[ts2phc.0.config] ens2f0 frequency_status 4 offset 0 phase_status 4 pps_status 0 s1",
			desc:             "dpll 2 is on Holdover, where source is lost, moving to holdover state",
		},
		{ // 2nd card spiking stay in holdover
			processName:      event.TS2PHCProcessName,
			cfgName:          "ts2phc.0.config",
			clockState:       event.PTP_FREERUN,
			outOfSpec:        false,
			iface:            "ens2f0",
			values:           map[event.ValueType]interface{}{event.OFFSET: int64(5000), event.PPS_STATUS: 0},
			wantGMState:      "GM[0]:[ts2phc.0.config] ens1f0 T-GM-STATUS s1",
			wantClockState:   "ptp4l[0]:[ts2phc.0.config] CLOCK_CLASS_CHANGE 7",
			wantProcessState: "ts2phc[0]:[ts2phc.0.config] ens2f0 offset 5000 pps_status 0 s0",
			desc:             "2nd card ts2phc offset spiked when in holdover",
		},
	}

	eChannel := make(chan event.Event, 100)
	eventManager := clockmgr.Init("node", eChannel, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eventManager.ProcessEvents(ctx)
	gmClk, err := clock.NewGM("ts2phc.0.config", leap.GetUtcOffset, pmcMock)
	require.NoError(t, err)
	addErr := eventManager.AddClock(gmClk)
	assert.NoError(t, addErr)
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
		assert.Nil(t, leap.LeapMgr)
	}()
	time.Sleep(1 * time.Second)
	for _, test := range tests {
		select {
		case eChannel <- sendEvents(test.cfgName, test.iface, test.processName, test.clockState, test.values, test.outOfSpec, test.sourceLost):
			log.Println("sent data to channel")
			log.Println(test.cfgName, test.processName, test.clockState, test.outOfSpec, test.values)
			time.Sleep(1 * time.Second)
		default:
			log.Println("nothing to read")
		}
	}

	time.Sleep(1 * time.Second)
}

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

func sendEvents(cfgName string, iface string, processName event.EventSource, state event.PTPState,
	values map[event.ValueType]interface{}, outOfSpec bool, sourceLost bool) event.Event {
	glog.Info("sending Nav status event to event handler Process")
	e := event.Event{
		Source:     processName,
		IFace:      iface,
		CfgName:    cfgName,
		ClockType:  "GM",
		Time:       0,
		WriteToLog: true,
		Reset:      false,
	}
	if processName == event.GNSS {
		var gpsStatus int64
		if gps, ok := values[event.GPS_STATUS]; ok {
			gpsStatus = int64(gps.(int))
		}
		var offset int64
		if off, ok := values[event.OFFSET]; ok {
			offset = off.(int64)
		}
		e.Data = &event.GNSSData{GPSStatus: gpsStatus, Offset: offset, SourceLost: sourceLost}
	} else {
		e.Data = &event.PTPData{
			State:      state,
			Values:     values,
			OutOfSpec:  outOfSpec,
			SourceLost: sourceLost,
		}
	}
	return e
}

func sendBCEvent(cfgName string, processName event.EventSource,
	state event.PTPState, values map[event.ValueType]interface{},
	sourceLost bool) event.Event {
	return event.Event{
		Source:     processName,
		IFace:      "ens1f0",
		CfgName:    cfgName,
		ClockType:  event.TBC,
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data: &event.PTPData{
			State:      state,
			Values:     values,
			SourceLost: sourceLost,
		},
	}
}

func TestTBCClockClassThroughProcessEvents(t *testing.T) {
	pmcMock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:    6,
				GrandmasterClockAccuracy: 0x21,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	eChannel := make(chan event.Event, 100)
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
	}()

	eventManager := clockmgr.Init("node", eChannel, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eventManager.ProcessEvents(ctx)

	tbcClk, err := clock.NewTBC(testBCCfgPTP4l, leap.GetUtcOffset, pmcMock)
	require.NoError(t, err)
	addErr := eventManager.AddClock(tbcClk)
	assert.NoError(t, addErr)
	eChannel <- event.Event{
		Source:    event.PMC,
		CfgName:   testBCCfgPTP4l,
		ClockType: event.TBC,
		Time:      time.Now().UnixMilli(),
		Data: &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{
			GrandmasterClockClass:    6,
			GrandmasterClockAccuracy: 0x21,
		}},
	}

	time.Sleep(500 * time.Millisecond)

	const (
		bcCfgDPLL  = testBCCfgTS2PHC
		bcCfgPTP4l = testBCCfgPTP4l
	)

	// First DPLL event carries leading source configuration.
	// AddEvent's first call creates the detail but does NOT insert into the
	// window, so we need WindowSize+1 events per source to fill the window.
	eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.LeadingSource:            true,
			event.InSyncConditionThreshold: uint64(10000),
			event.InSyncConditionTimes:     uint64(1),
			event.ToFreeRunThreshold:       uint64(1500),
			event.MaxInSpecOffset:          uint64(500),
			event.OFFSET:                   int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	// 10 more DPLL events to fill the DPLL window (first event created the detail,
	// these 10 fill WindowSize=10)
	for i := 0; i < 10; i++ {
		eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	// First PTP4l event carries downstream port configuration
	eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.ControlledPortsConfig: testBCCfgPTP4l,
			event.ClockIDKey:            testClockID,
			event.OFFSET:                int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	// 10 more PTP4l events to fill the PTP4l window; the last triggers FREERUN→LOCKED
	for i := 0; i < 10; i++ {
		eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	// One more DPLL event in LOCKED state triggers "stay LOCKED" path:
	// upstream ParentDataSet (class 6) != downstream (class 0) → needsDownstreamUpdate
	// → downstreamAnnounceIWF → announceClockClass(6)
	eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)

	assert.True(t, waitForClockClass(eventManager.GetClock(testBCCfgPTP4l), 6, 5*time.Second),
		"expected clock class 6 after BC reaches LOCKED")

	// Phase 2: PTP4l source lost triggers LOCKED→HOLDOVER (class 135)
	eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_FREERUN,
		map[event.ValueType]interface{}{event.OFFSET: int64(10)}, true)

	assert.True(t, waitForClockClass(eventManager.GetClock(testBCCfgPTP4l), 135, 5*time.Second),
		"expected clock class 135 after BC enters HOLDOVER")
}

func waitForMetric(gauge *prometheus.GaugeVec, cfgName string, expected float64, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		val := testutil.ToFloat64(gauge.With(prometheus.Labels{
			"process": "ptp4l", "config": cfgName, "node": "testnode", //nolint:goconst
		}))
		if val == expected {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func waitForClockClass(clk clock.Clock, expected fbprotocol.ClockClass, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		if clk.ClockClass() == expected {
			return true
		}
		select {
		case <-deadline:
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestTBCClockClassMetric(t *testing.T) {
	pmcMock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:    6,
				GrandmasterClockAccuracy: 0x21,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
	}()

	clockClassGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "test_ptp_clock_class",
			Help: "test clock class metric",
		},
		[]string{"process", "config", "node"},
	)
	offsetGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "test_ptp_offset",
			Help: "test offset metric",
		},
		[]string{testFromLabel, "process", "node", testIfaceLabel},
	)
	clockStateGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "test_ptp_clock_state",
			Help: "test clock state metric",
		},
		[]string{"process", "node", testIfaceLabel},
	)

	eChannel := make(chan event.Event, 100)
	eventManager := clockmgr.Init("testnode", eChannel, offsetGauge, clockStateGauge, clockClassGauge, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eventManager.ProcessEvents(ctx)

	tbcClk, err := clock.NewTBC(testBCCfgPTP4l, leap.GetUtcOffset, pmcMock)
	require.NoError(t, err)
	addErr := eventManager.AddClock(tbcClk)
	assert.NoError(t, addErr)
	eChannel <- event.Event{
		Source:    event.PMC,
		CfgName:   testBCCfgPTP4l,
		ClockType: event.TBC,
		Time:      time.Now().UnixMilli(),
		Data: &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{
			GrandmasterClockClass:    6,
			GrandmasterClockAccuracy: 0x21,
		}},
	}

	time.Sleep(500 * time.Millisecond)

	const (
		bcCfgDPLL  = testBCCfgTS2PHC
		bcCfgPTP4l = testBCCfgPTP4l
	)

	// Fill DPLL window: first event creates detail, next 10 fill WindowSize=10
	eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.LeadingSource:            true,
			event.InSyncConditionThreshold: uint64(10000),
			event.InSyncConditionTimes:     uint64(1),
			event.ToFreeRunThreshold:       uint64(1500),
			event.MaxInSpecOffset:          uint64(500),
			event.OFFSET:                   int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 10; i++ {
		eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	// Fill PTP4l window
	eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.ControlledPortsConfig: testBCCfgPTP4l,
			event.ClockIDKey:            testClockID,
			event.OFFSET:                int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 10; i++ {
		eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	// Trigger "stay LOCKED" → upstream data mismatch → announceClockClass(6)
	eChannel <- sendBCEvent(bcCfgDPLL, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)

	assert.True(t, waitForMetric(clockClassGauge, bcCfgPTP4l, 6, 5*time.Second),
		"expected clock class metric = 6 for %s after LOCKED", bcCfgPTP4l)

	// PTP4l source lost → LOCKED→HOLDOVER (class 135)
	eChannel <- sendBCEvent(bcCfgPTP4l, event.PTP4l, event.PTP_FREERUN,
		map[event.ValueType]interface{}{event.OFFSET: int64(10)}, true)

	assert.True(t, waitForMetric(clockClassGauge, bcCfgPTP4l, 135, 5*time.Second),
		"expected clock class metric = 135 for %s after HOLDOVER", bcCfgPTP4l)
}

// drainIPCMessages collects all IPC messages available on the channel within the timeout.
func drainIPCMessages(ch <-chan ipc.Message, timeout time.Duration) []ipc.Message {
	var msgs []ipc.Message
	deadline := time.After(timeout)
	for {
		select {
		case msg := <-ch:
			msgs = append(msgs, msg)
		case <-deadline:
			return msgs
		}
	}
}

// waitForIPCMessage waits for an IPC message matching the given type and returns it.
func waitForIPCMessage(ch <-chan ipc.Message, msgType string, timeout time.Duration) (ipc.Message, bool) {
	deadline := time.After(timeout)
	for {
		select {
		case msg := <-ch:
			if msg.Type == msgType {
				return msg, true
			}
		case <-deadline:
			return ipc.Message{}, false
		}
	}
}

// startIPCSocketListener creates a Unix socket, starts an ipc.Link draining
// the cache, and returns a channel of decoded messages read from the socket.
// The returned cleanup function must be deferred.
func startIPCSocketListener(t *testing.T, cache *ipc.Cache) (<-chan ipc.Message, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ipc-test")
	require.NoError(t, err)
	socketPath := filepath.Join(dir, "ipc.sock")

	listener, err := Listen(socketPath)
	require.NoError(t, err)

	msgCh := make(chan ipc.Message, 100)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-ctx.Done()
		cancel()
	}()
	go ipc.NewLink(socketPath, cache).Run(ctx)

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				dec := json.NewDecoder(c)
				for {
					var msg ipc.Message
					if decErr := dec.Decode(&msg); decErr != nil {
						return
					}
					msgCh <- msg
				}
			}(conn)
		}
	}()

	cleanup := func() {
		cancel()
		listener.Close()
		os.RemoveAll(dir)
	}

	return msgCh, cleanup
}

func TestMultiClockIPCIsolation(t *testing.T) {
	pmcMock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:    6,
				GrandmasterClockAccuracy: 0x21,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
		GMSettingsResult: protocol.GrandmasterSettings{
			TimePropertiesDS: protocol.TimePropertiesDS{
				TimeTraceable:      true,
				FrequencyTraceable: true,
			},
		},
	}
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
	}()

	clockClassGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_multi_ipc_clock_class", Help: testHelpStr},
		[]string{"process", "config", "node"},
	)
	offsetGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_multi_ipc_offset", Help: testHelpStr},
		[]string{testFromLabel, "process", "node", testIfaceLabel},
	)
	clockStateGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_multi_ipc_clock_state", Help: testHelpStr},
		[]string{"process", "node", testIfaceLabel},
	)

	cache := ipc.NewCache(100)
	eChannel := make(chan event.Event, 100)
	eventManager := clockmgr.Init("node", eChannel, offsetGauge, clockStateGauge, clockClassGauge, cache)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eventManager.ProcessEvents(ctx)

	socketCh, cleanup := startIPCSocketListener(t, cache)
	defer cleanup()

	const (
		gmCfg    = "ts2phc.0.config"
		gmIface  = "ens1f0"
		tbcPTP4l = "ptp4l.1.config"
		tbcTS2HC = "ts2phc.1.config"
		tbcIface = "ens2f0"
	)

	// Register GM clock
	gmClk, err := clock.NewGM(gmCfg, leap.GetUtcOffset, pmcMock)
	require.NoError(t, err)
	addErr := eventManager.AddClock(gmClk)
	require.NoError(t, addErr)

	// Register T-BC clock
	tbcClk, err := clock.NewTBC(tbcPTP4l, leap.GetUtcOffset, pmcMock)
	require.NoError(t, err)
	addErr = eventManager.AddClock(tbcClk)
	require.NoError(t, addErr)
	eChannel <- event.Event{
		Source:    event.PMC,
		CfgName:   tbcPTP4l,
		ClockType: event.TBC,
		Time:      time.Now().UnixMilli(),
		Data: &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{
			GrandmasterClockClass:    6,
			GrandmasterClockAccuracy: 0x21,
		}},
	}

	time.Sleep(200 * time.Millisecond)

	// --- Helpers ---

	assertNoProfile := func(t *testing.T, msgs []ipc.Message, profile, phase string) {
		t.Helper()
		for _, msg := range msgs {
			if msg.Profile == profile {
				t.Errorf("%s: unexpected IPC message for profile %s: type=%s values=%v",
					phase, profile, msg.Type, msg.Values)
			}
		}
	}

	sendGM := func(process event.EventSource, state event.PTPState, sourceLost bool) event.Event {
		return sendEvents(gmCfg, gmIface, process, state,
			map[event.ValueType]interface{}{event.OFFSET: int64(0), event.GPS_STATUS: 3},
			false, sourceLost)
	}

	sendTBC := func(cfgName string, processName event.EventSource,
		state event.PTPState, values map[event.ValueType]interface{},
		sourceLost bool) event.Event {
		return event.Event{
			Source:     processName,
			IFace:      tbcIface,
			CfgName:    cfgName,
			ClockType:  event.TBC,
			Time:       time.Now().UnixMilli(),
			WriteToLog: true,
			Data: &event.PTPData{
				State:      state,
				Values:     values,
				SourceLost: sourceLost,
			},
		}
	}

	// =====================================================================
	// Phase 1: GM FREERUN → LOCKED (GNSS/DPLL toggling)
	// =====================================================================

	eChannel <- sendGM(event.DPLL, event.PTP_LOCKED, false)
	eChannel <- sendGM(event.GNSS, event.PTP_LOCKED, false)
	time.Sleep(100 * time.Millisecond)

	eChannel <- sendGM(event.TS2PHCProcessName, event.PTP_FREERUN, false)
	time.Sleep(100 * time.Millisecond)

	drainIPCMessages(socketCh, 500*time.Millisecond)

	eChannel <- sendGM(event.TS2PHCProcessName, event.PTP_LOCKED, false)

	msgs := drainIPCMessages(socketCh, 2*time.Second)

	var gotGMPTPStateLocked bool
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			sv, ok := msg.Values.(ipc.StateValue)
			if ok && sv.State == ipc.StateLocked {
				gotGMPTPStateLocked = true
				assert.Equal(t, gmCfg, msg.Profile, "phase1: ptp_state profile should match GM config")
			}
		}
	}
	assert.True(t, gotGMPTPStateLocked, "phase1: expected ptp_state LOCKED for GM")
	assertNoProfile(t, msgs, tbcPTP4l, "phase1")

	// =====================================================================
	// Phase 2: GM LOCKED → HOLDOVER (DPLL holdover)
	// =====================================================================

	drainIPCMessages(socketCh, 200*time.Millisecond)

	eChannel <- sendGM(event.DPLL, event.PTP_HOLDOVER, false)

	msgs = drainIPCMessages(socketCh, 2*time.Second)

	var gotGMStateHoldover, gotGMClockClass7 bool
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			sv, ok := msg.Values.(ipc.StateValue)
			if ok && sv.State == ipc.StateHoldover {
				gotGMStateHoldover = true
			}
		}
		if msg.Type == ipc.TypeClockClass {
			cv, ok := msg.Values.(ipc.ClockClassValue)
			if ok && cv.ClockClass == 7 {
				gotGMClockClass7 = true
			}
		}
	}
	assert.True(t, gotGMStateHoldover, "phase2: expected ptp_state HOLDOVER for GM")
	assert.True(t, gotGMClockClass7, "phase2: expected clock_class 7 for GM")
	assertNoProfile(t, msgs, tbcPTP4l, "phase2")

	// =====================================================================
	// Phase 3: GM HOLDOVER → FREERUN (DPLL freerun)
	// =====================================================================

	drainIPCMessages(socketCh, 200*time.Millisecond)

	eChannel <- sendGM(event.DPLL, event.PTP_FREERUN, false)

	msgs = drainIPCMessages(socketCh, 2*time.Second)

	var gotGMStateFreerun, gotGMClockClass248 bool
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			sv, ok := msg.Values.(ipc.StateValue)
			if ok && sv.State == ipc.StateFreerun {
				gotGMStateFreerun = true
			}
		}
		if msg.Type == ipc.TypeClockClass {
			cv, ok := msg.Values.(ipc.ClockClassValue)
			if ok && cv.ClockClass == 248 {
				gotGMClockClass248 = true
			}
		}
	}
	assert.True(t, gotGMStateFreerun, "phase3: expected ptp_state FREERUN for GM")
	assert.True(t, gotGMClockClass248, "phase3: expected clock_class 248 for GM")
	assertNoProfile(t, msgs, tbcPTP4l, "phase3")

	// =====================================================================
	// Phase 4: GM dedup check
	// =====================================================================

	drainIPCMessages(socketCh, 200*time.Millisecond)

	for i := 0; i < 5; i++ {
		eChannel <- sendGM(event.DPLL, event.PTP_FREERUN, false)
		time.Sleep(50 * time.Millisecond)
	}

	dedup := drainIPCMessages(socketCh, 500*time.Millisecond)
	assert.Empty(t, dedup, "phase4: duplicate GM events should not produce IPC messages")

	// =====================================================================
	// Phase 5: T-BC FREERUN → LOCKED
	// =====================================================================

	drainIPCMessages(socketCh, 200*time.Millisecond)

	eChannel <- sendTBC(tbcTS2HC, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.LeadingSource:            true,
			event.InSyncConditionThreshold: uint64(10000),
			event.InSyncConditionTimes:     uint64(1),
			event.ToFreeRunThreshold:       uint64(1500),
			event.MaxInSpecOffset:          uint64(500),
			event.OFFSET:                   int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 10; i++ {
		eChannel <- sendTBC(tbcTS2HC, event.DPLL, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	eChannel <- sendTBC(tbcPTP4l, event.PTP4l, event.PTP_LOCKED,
		map[event.ValueType]interface{}{
			event.ControlledPortsConfig: tbcPTP4l,
			event.ClockIDKey:            testClockID,
			event.OFFSET:                int64(10),
		}, false)
	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 10; i++ {
		eChannel <- sendTBC(tbcPTP4l, event.PTP4l, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	msgs = drainIPCMessages(socketCh, 2*time.Second)

	var gotTBCStateLocked bool
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			sv, ok := msg.Values.(ipc.StateValue)
			if ok && sv.State == ipc.StateLocked {
				gotTBCStateLocked = true
				assert.Equal(t, tbcPTP4l, msg.Profile, "phase5: ptp_state profile should match T-BC config")
				assert.Equal(t, tbcIface, msg.IFace, "phase5: ptp_state iface should be T-BC interface")
			}
		}
	}
	assert.True(t, gotTBCStateLocked, "phase5: expected ptp_state LOCKED for T-BC")
	assertNoProfile(t, msgs, gmCfg, "phase5")

	// =====================================================================
	// Phase 6: T-BC LOCKED → HOLDOVER (source lost)
	// =====================================================================

	drainIPCMessages(socketCh, 200*time.Millisecond)

	eChannel <- sendTBC(tbcPTP4l, event.PTP4l, event.PTP_FREERUN,
		map[event.ValueType]interface{}{event.OFFSET: int64(10)}, true)

	msgs = drainIPCMessages(socketCh, 2*time.Second)

	var gotTBCStateHoldover, gotTBCClockClass135 bool
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			sv, ok := msg.Values.(ipc.StateValue)
			if ok && sv.State == ipc.StateHoldover {
				gotTBCStateHoldover = true
			}
		}
		if msg.Type == ipc.TypeClockClass {
			cv, ok := msg.Values.(ipc.ClockClassValue)
			if ok && cv.ClockClass == 135 {
				gotTBCClockClass135 = true
			}
		}
	}
	assert.True(t, gotTBCStateHoldover, "phase6: expected ptp_state HOLDOVER for T-BC")
	assert.True(t, gotTBCClockClass135, "phase6: expected clock_class 135 for T-BC")
	assertNoProfile(t, msgs, gmCfg, "phase6")

	// =====================================================================
	// Phase 7: T-BC HOLDOVER → FREERUN (large offset)
	// =====================================================================

	drainIPCMessages(socketCh, 200*time.Millisecond)

	eChannel <- sendTBC(tbcTS2HC, event.DPLL, event.PTP_LOCKED,
		map[event.ValueType]interface{}{event.OFFSET: int64(50000)}, false)

	msgs = drainIPCMessages(socketCh, 2*time.Second)

	var gotTBCStateFreerun, gotTBCClockClass248 bool
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			sv, ok := msg.Values.(ipc.StateValue)
			if ok && sv.State == ipc.StateFreerun {
				gotTBCStateFreerun = true
			}
		}
		if msg.Type == ipc.TypeClockClass {
			cv, ok := msg.Values.(ipc.ClockClassValue)
			if ok && cv.ClockClass == 248 {
				gotTBCClockClass248 = true
			}
		}
	}
	assert.True(t, gotTBCStateFreerun, "phase7: expected ptp_state FREERUN for T-BC")
	assert.True(t, gotTBCClockClass248, "phase7: expected clock_class 248 for T-BC")
	assertNoProfile(t, msgs, gmCfg, "phase7")

	// =====================================================================
	// Phase 8: T-BC dedup check
	// =====================================================================

	drainIPCMessages(socketCh, 200*time.Millisecond)

	for i := 0; i < 5; i++ {
		eChannel <- sendTBC(tbcTS2HC, event.DPLL, event.PTP_LOCKED,
			map[event.ValueType]interface{}{event.OFFSET: int64(50000)}, false)
		time.Sleep(50 * time.Millisecond)
	}

	dedup = drainIPCMessages(socketCh, 500*time.Millisecond)
	assert.Empty(t, dedup, "phase8: duplicate T-BC events should not produce IPC messages")
}

func TestOverallClockStateIntegration(t *testing.T) {
	pmcMock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:    6,
				GrandmasterClockAccuracy: 0x21,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
	}()

	clockClassGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_overall_clock_class", Help: testHelpStr},
		[]string{"process", "config", "node"},
	)
	offsetGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_overall_offset", Help: testHelpStr},
		[]string{testFromLabel, "process", "node", testIfaceLabel},
	)
	clockStateGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_overall_clock_state", Help: testHelpStr},
		[]string{"process", "node", testIfaceLabel},
	)

	cache := ipc.NewCache(100)
	eChannel := make(chan event.Event, 100)
	eventManager := clockmgr.Init("node", eChannel, offsetGauge, clockStateGauge, clockClassGauge, cache)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eventManager.ProcessEvents(ctx)

	socketCh, cleanup := startIPCSocketListener(t, cache)
	defer cleanup()

	// Register two TBCClocks
	tbcClk1, err := clock.NewTBC(testBCCfgPTP4l, leap.GetUtcOffset, pmcMock)
	require.NoError(t, err)
	err = eventManager.AddClock(tbcClk1)
	require.NoError(t, err)
	eChannel <- event.Event{
		Source: event.PMC, CfgName: testBCCfgPTP4l, ClockType: event.TBC,
		Time: time.Now().UnixMilli(),
		Data: &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{
			GrandmasterClockClass: 6, GrandmasterClockAccuracy: 0x21,
		}},
	}

	secondTBCCfgName := "ptp4l.1.config"
	tbcClk2, err := clock.NewTBC(secondTBCCfgName, leap.GetUtcOffset, pmcMock)
	require.NoError(t, err)
	err = eventManager.AddClock(tbcClk2)
	require.NoError(t, err)
	eChannel <- event.Event{
		Source: event.PMC, CfgName: secondTBCCfgName, ClockType: event.TBC,
		Time: time.Now().UnixMilli(),
		Data: &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{
			GrandmasterClockClass: 6, GrandmasterClockAccuracy: 0x21,
		}},
	}

	time.Sleep(200 * time.Millisecond)

	// --- Phase 1: Lock both TBCClocks' PTP state ---

	lockTBCClock := func(cfgDPLL, cfgPTP4l string) {
		eChannel <- sendBCEvent(cfgDPLL, event.DPLL, event.PTP_LOCKED,
			map[event.ValueType]interface{}{
				event.LeadingSource:            true,
				event.InSyncConditionThreshold: uint64(10000),
				event.InSyncConditionTimes:     uint64(1),
				event.ToFreeRunThreshold:       uint64(1500),
				event.MaxInSpecOffset:          uint64(500),
				event.OFFSET:                   int64(10),
			}, false)
		time.Sleep(100 * time.Millisecond)

		for i := 0; i < 10; i++ {
			eChannel <- sendBCEvent(cfgDPLL, event.DPLL, event.PTP_LOCKED,
				map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
			time.Sleep(50 * time.Millisecond)
		}

		eChannel <- sendBCEvent(cfgPTP4l, event.PTP4l, event.PTP_LOCKED,
			map[event.ValueType]interface{}{
				event.ControlledPortsConfig: cfgPTP4l,
				event.ClockIDKey:            testClockID,
				event.OFFSET:                int64(10),
			}, false)
		time.Sleep(100 * time.Millisecond)

		for i := 0; i < 10; i++ {
			eChannel <- sendBCEvent(cfgPTP4l, event.PTP4l, event.PTP_LOCKED,
				map[event.ValueType]interface{}{event.OFFSET: int64(10)}, false)
			time.Sleep(50 * time.Millisecond)
		}
	}

	lockTBCClock(testBCCfgTS2PHC, testBCCfgPTP4l)
	lockTBCClock("ts2phc.1.config", secondTBCCfgName)

	// Drain all messages from PTP locking phase via the CEPv2 socket
	msgs := drainIPCMessages(socketCh, 2*time.Second)

	// Verify we got ptp_state LOCKED but sync_state should be FREERUN
	// (os clock is still FREERUN since no PHC2SYS event yet)
	var gotPTPLocked int
	var gotSyncStateFreerun int
	for _, msg := range msgs {
		if msg.Type == ipc.TypePTPState {
			if sv, ok := msg.Values.(ipc.StateValue); ok && sv.State == ipc.StateLocked {
				gotPTPLocked++
			}
		}
		if msg.Type == ipc.TypeSyncState {
			if sv, ok := msg.Values.(ipc.SyncStateValue); ok && sv.State == ipc.StateFreerun {
				gotSyncStateFreerun++
			}
		}
	}
	assert.GreaterOrEqual(t, gotPTPLocked, 2, "expected ptp_state LOCKED for both profiles")

	// --- Phase 2: Send PHC2SYS LOCKED event ---

	drainIPCMessages(socketCh, 200*time.Millisecond)

	eChannel <- event.Event{
		Source:    event.PHC2SYS,
		IFace:     "CLOCK_REALTIME",
		CfgName:   testBCCfgPTP4l,
		ClockType: event.TBC,
		Time:      time.Now().UnixMilli(),
		Data: &event.PTPData{
			State:  event.PTP_LOCKED,
			Values: map[event.ValueType]interface{}{event.OFFSET: int64(5)},
		},
	}

	msgs = drainIPCMessages(socketCh, 2*time.Second)

	var osClockCount int
	var syncStateLocked int
	syncStateProfiles := map[string]bool{}
	for _, msg := range msgs {
		if msg.Type == ipc.TypeOSClockState {
			osClockCount++
			sv, ok := msg.Values.(ipc.StateValue)
			require.True(t, ok)
			assert.Equal(t, ipc.StateLocked, sv.State)
		}
		if msg.Type == ipc.TypeSyncState {
			sv, ok := msg.Values.(ipc.SyncStateValue)
			if ok && sv.State == ipc.StateLocked {
				syncStateLocked++
				syncStateProfiles[msg.Profile] = true
			}
		}
	}
	assert.Equal(t, 1, osClockCount, "os_clock_state should be emitted exactly once")
	assert.Equal(t, 2, syncStateLocked, "sync_state LOCKED should be emitted for both profiles")
	assert.True(t, syncStateProfiles[testBCCfgPTP4l], "sync_state should include profile 0")
	assert.True(t, syncStateProfiles[secondTBCCfgName], "sync_state should include profile 1")

	// --- Phase 3: Send PHC2SYS FREERUN → overall drops to FREERUN ---

	drainIPCMessages(socketCh, 200*time.Millisecond)

	eChannel <- event.Event{
		Source:    event.PHC2SYS,
		IFace:     "CLOCK_REALTIME",
		CfgName:   testBCCfgPTP4l,
		ClockType: event.TBC,
		Time:      time.Now().UnixMilli(),
		Data: &event.PTPData{
			State:  event.PTP_FREERUN,
			Values: map[event.ValueType]interface{}{event.OFFSET: int64(0)},
		},
	}

	msgs = drainIPCMessages(socketCh, 2*time.Second)

	var syncStateFreerunCount int
	for _, msg := range msgs {
		if msg.Type == ipc.TypeSyncState {
			sv, ok := msg.Values.(ipc.SyncStateValue)
			if ok && sv.State == ipc.StateFreerun {
				syncStateFreerunCount++
			}
		}
	}
	assert.Equal(t, 2, syncStateFreerunCount, "sync_state FREERUN should be emitted for both profiles")

	// --- Phase 4: Duplicate PHC2SYS FREERUN → no new messages on socket ---

	drainIPCMessages(socketCh, 200*time.Millisecond)

	eChannel <- event.Event{
		Source:    event.PHC2SYS,
		IFace:     "CLOCK_REALTIME",
		CfgName:   testBCCfgPTP4l,
		ClockType: event.TBC,
		Time:      time.Now().UnixMilli(),
		Data: &event.PTPData{
			State:  event.PTP_FREERUN,
			Values: map[event.ValueType]interface{}{event.OFFSET: int64(0)},
		},
	}

	dedup := drainIPCMessages(socketCh, 500*time.Millisecond)
	osClockDedup := 0
	syncStateDedup := 0
	for _, msg := range dedup {
		if msg.Type == ipc.TypeOSClockState {
			osClockDedup++
		}
		if msg.Type == ipc.TypeSyncState {
			syncStateDedup++
		}
	}
	assert.Equal(t, 0, osClockDedup, "duplicate os_clock_state should not be emitted on socket")
	assert.Equal(t, 0, syncStateDedup, "duplicate sync_state should not be emitted on socket")
}

func TestSyncEIPCIntegration(t *testing.T) {
	pmcMock := &pmc.MockClient{
		ParentTimeCurrentDSResult: pmc.ParentTimeCurrentDS{
			ParentDataSet: protocol.ParentDataSet{
				GrandmasterClockClass:    6,
				GrandmasterClockAccuracy: 0x21,
			},
			TimePropertiesDS: protocol.TimePropertiesDS{
				CurrentUtcOffset:      37,
				CurrentUtcOffsetValid: true,
				PtpTimescale:          true,
				TimeTraceable:         true,
			},
			CurrentDS: protocol.CurrentDS{StepsRemoved: 1},
		},
	}
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		time.Sleep(100 * time.Millisecond)
	}()

	clockClassGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_synce_clock_class", Help: testHelpStr},
		[]string{"process", "config", "node"},
	)
	offsetGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_synce_offset", Help: testHelpStr},
		[]string{testFromLabel, "process", "node", testIfaceLabel},
	)
	clockStateGauge := prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "test_synce_clock_state", Help: testHelpStr},
		[]string{"process", "node", testIfaceLabel},
	)

	cache := ipc.NewCache(100)
	eChannel := make(chan event.Event, 100)
	eventManager := clockmgr.Init("node", eChannel, offsetGauge, clockStateGauge, clockClassGauge, cache)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eventManager.ProcessEvents(ctx)

	socketCh, cleanup := startIPCSocketListener(t, cache)
	defer cleanup()

	tbcClk, err := clock.NewTBC(testBCCfgPTP4l, leap.GetUtcOffset, pmcMock)
	require.NoError(t, err)
	err = eventManager.AddClock(tbcClk)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	// --- Phase 1: SyncE state event → synce_state arrives on socket ---

	eChannel <- event.Event{
		Source:     event.SYNCE,
		IFace:      "ens7f0",
		CfgName:    "synce4l.0.config",
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data: &event.PTPData{
			State: event.PTP_LOCKED,
			Values: map[event.ValueType]interface{}{
				event.EEC_STATE:      "EEC_LOCKED",
				event.DEVICE:         "synce1",
				event.NETWORK_OPTION: 1,
			},
		},
	}

	msg, ok := waitForIPCMessage(socketCh, ipc.TypeSyncEState, 2*time.Second)
	assert.True(t, ok, "expected synce_state message on socket")
	if ok {
		assert.Equal(t, testBCCfgPTP4l, msg.Profile)
		assert.Equal(t, "ens7f0", msg.IFace)
		sv, svOK := msg.Values.(ipc.SyncEStateValue)
		require.True(t, svOK)
		assert.Equal(t, "EEC_LOCKED", sv.State)
	}

	// --- Phase 2: SyncE clock quality event → synce_clock_quality arrives on socket ---

	drainIPCMessages(socketCh, 200*time.Millisecond)

	eChannel <- event.Event{
		Source:     event.SYNCE,
		IFace:      "ens7f0",
		CfgName:    "synce4l.0.config",
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data: &event.PTPData{
			State: event.PTP_LOCKED,
			Values: map[event.ValueType]interface{}{
				event.QL:             byte(4),
				event.EXT_QL:         byte(0xFF),
				event.CLOCK_QUALITY:  "PRS",
				event.DEVICE:         "synce1",
				event.NETWORK_OPTION: 1,
			},
		},
	}

	msg, ok = waitForIPCMessage(socketCh, ipc.TypeSyncEClockQuality, 2*time.Second)
	assert.True(t, ok, "expected synce_clock_quality message on socket")
	if ok {
		assert.Equal(t, testBCCfgPTP4l, msg.Profile)
		qv, qvOK := msg.Values.(ipc.SyncEClockQualityValue)
		require.True(t, qvOK)
		assert.Equal(t, 4, qv.QL)
		assert.Equal(t, 0xFF, qv.ExtendedQL)
	}

	// --- Phase 3: Verify SyncE does NOT produce ptp_state or sync_state on socket ---

	drainIPCMessages(socketCh, 200*time.Millisecond)

	eChannel <- event.Event{
		Source:     event.SYNCE,
		IFace:      "ens7f0",
		CfgName:    "synce4l.0.config",
		Time:       time.Now().UnixMilli(),
		WriteToLog: true,
		Data: &event.PTPData{
			State: event.PTP_FREERUN,
			Values: map[event.ValueType]interface{}{
				event.EEC_STATE:      "EEC_FREERUN",
				event.DEVICE:         "synce1",
				event.NETWORK_OPTION: 1,
			},
		},
	}

	msgs := drainIPCMessages(socketCh, 1*time.Second)
	var foundSyncEFreerun bool
	for _, m := range msgs {
		assert.NotEqual(t, ipc.TypePTPState, m.Type, "SyncE should not produce ptp_state on socket")
		assert.NotEqual(t, ipc.TypeSyncState, m.Type, "SyncE should not produce sync_state on socket")
		if m.Type == ipc.TypeSyncEState {
			sv, svOK := m.Values.(ipc.SyncEStateValue)
			if svOK && sv.State == "EEC_FREERUN" {
				foundSyncEFreerun = true
			}
		}
	}
	assert.True(t, foundSyncEFreerun, "expected synce_state EEC_FREERUN on socket")
}

func TestApplyingSkipsTBCEvents(t *testing.T) {
	const cfg = "ptp4l.0.config"

	eChannel := make(chan event.Event, 10)
	em := clockmgr.Init("node", eChannel, nil, nil, nil, nil)

	pmcMock := &pmc.MockClient{}
	tbcClk, err := clock.NewTBC(testBCCfgPTP4l, leap.GetUtcOffset, pmcMock)
	require.NoError(t, err)
	err = em.AddClock(tbcClk)
	require.NoError(t, err)

	clk := em.GetClock(cfg)
	require.NotNil(t, clk)

	// Set applying BEFORE starting ProcessEvents
	em.SetApplying(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go em.ProcessEvents(ctx)

	// Send a DPLL event that would normally be processed
	eChannel <- event.Event{
		Source: event.DPLL, IFace: "ens1f0", CfgName: cfg, ClockType: event.TBC,
		Time: time.Now().UnixMilli(), WriteToLog: true,
		Data: &event.PTPData{
			State:  event.PTP_LOCKED,
			Values: map[event.ValueType]interface{}{event.OFFSET: int64(5), event.LeadingSource: true},
		},
	}
	time.Sleep(200 * time.Millisecond)

	// Clock class should remain 0 (uninitialized) — event was skipped
	assert.Equal(t, fbprotocol.ClockClass(0), clk.ClockClass(),
		"TBC events must be skipped while applying")

	// Clear applying and send the same event — now it should be processed
	em.SetApplying(false)
	eChannel <- event.Event{
		Source: event.DPLL, IFace: "ens1f0", CfgName: cfg, ClockType: event.TBC,
		Time: time.Now().UnixMilli(), WriteToLog: true,
		Data: &event.PTPData{
			State:  event.PTP_LOCKED,
			Values: map[event.ValueType]interface{}{event.OFFSET: int64(5), event.LeadingSource: true},
		},
	}
	time.Sleep(200 * time.Millisecond)

	// Now the clock should have processed the event (clock class may still be uninitialized
	// but the internal data should have been populated)
	clkInternal := em.GetClock(cfg)
	assert.NotNil(t, clkInternal, "clock should still be registered")

	time.Sleep(50 * time.Millisecond)
}
