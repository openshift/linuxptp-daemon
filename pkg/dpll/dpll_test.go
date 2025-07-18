package dpll_test

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	nl "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
)

const (
	clockid    = 123454566
	id         = 123456
	moduleName = "test"
)

func TestMain(m *testing.M) {
	teardownTests := testhelpers.SetupTests()
	defer teardownTests()
	os.Exit(m.Run())
}

type DpllTestCase struct {
	reply                     *nl.DoDeviceGetReply
	sourceLost                bool
	source                    event.EventSource
	offset                    int64
	expectedIntermediateState event.PTPState
	expectedState             event.PTPState
	sleep                     time.Duration
	expectedPhaseStatus       int64
	expectedPhaseOffset       int64
	expectedFrequencyStatus   int64
	expectedInSpecState       bool
	desc                      string
}

// mode 1: "manual",	2: "automatic", 3: "holdover", 4: "freerun",
// 1: "unlocked", 2: "locked", 3: "locked-ho-acquired", 4: "holdover"
func getTestData(source event.EventSource, pinType uint32) []DpllTestCase {
	return []DpllTestCase{{
		reply: &nl.DoDeviceGetReply{
			ID:            id,
			ModuleName:    moduleName,
			Mode:          1,
			ModeSupported: []uint32{0},
			LockStatus:    3, //LHAQ,
			ClockID:       clockid,
			Type:          2, //1 pps 2 eec
		},
		sourceLost:                false,
		source:                    source,
		offset:                    dpll.FaultyPhaseOffset,
		expectedIntermediateState: event.PTP_FREERUN,
		expectedState:             event.PTP_FREERUN,
		expectedPhaseStatus:       0, //no phase status event for eec
		expectedPhaseOffset:       dpll.FaultyPhaseOffset * 1000000,
		expectedFrequencyStatus:   3, // LHAQ
		expectedInSpecState:       false,
		desc:                      fmt.Sprintf("1.LHAQ frequency status, unknown Phase status : pin %d ", pinType),
	}, {
		reply: &nl.DoDeviceGetReply{
			ID:            id,
			ModuleName:    moduleName,
			Mode:          1,
			ModeSupported: []uint32{0},
			LockStatus:    3, //LHAQ,
			ClockID:       clockid,
			Type:          1, //1 pps 2 eec
		},
		sourceLost:                false,
		source:                    source,
		offset:                    1,
		expectedIntermediateState: event.PTP_LOCKED,
		expectedState:             event.PTP_LOCKED,
		expectedPhaseStatus:       3, //no phase status event for eec
		expectedPhaseOffset:       50,
		expectedFrequencyStatus:   3, // LHAQ
		expectedInSpecState:       true,
		desc:                      fmt.Sprintf("2. with LHAQ frequency status, reading phase status  : pin %d ", pinType),
	},
		{
			reply: &nl.DoDeviceGetReply{
				ID:            id,
				ModuleName:    moduleName,
				Mode:          1,
				ModeSupported: []uint32{0},
				LockStatus: func() uint32 {
					if pinType == 2 {
						return 4 // holdover
					} else {
						return 4 // holdover
					}
				}(), // holdover,
				ClockID: clockid,
				Type:    pinType, //1 pps 2 eec
			},
			sourceLost:                true,
			sleep:                     2,
			source:                    source,
			offset:                    1,
			expectedIntermediateState: event.PTP_HOLDOVER,
			expectedState:             event.PTP_FREERUN,
			expectedPhaseStatus: func() int64 {
				if pinType == 2 {
					return 3 // LHAQ
				} else {
					return 4 // holdover
				}
			}(), //no phase status event for eec
			expectedPhaseOffset: dpll.FaultyPhaseOffset * 1000000,
			expectedFrequencyStatus: func() int64 {
				if pinType == 2 {
					return 4
				} else {
					return 3
				}
			}(), // holdover to free run
			expectedInSpecState: func() bool {
				if pinType == 2 {
					return false
				} else {
					return true
				}
			}(),
			desc: fmt.Sprintf("3. Holdover frequency/Phase status status times out in 1sec  : pin %d ", pinType),
		},
		{
			reply: &nl.DoDeviceGetReply{
				ID:            id,
				ModuleName:    moduleName,
				Mode:          1,
				ModeSupported: []uint32{0},
				LockStatus:    4, // holdover,
				ClockID:       clockid,
				Type:          pinType, //1 pps 2 eec
			},
			sourceLost:                true,
			sleep:                     2,
			source:                    source,
			offset:                    6,
			expectedIntermediateState: event.PTP_FREERUN,
			expectedState:             event.PTP_FREERUN,
			expectedPhaseStatus: func() int64 {
				if pinType == 2 {
					return 3
				} else {
					return 4
				}
			}(), //no phase status event for eec
			expectedPhaseOffset: dpll.FaultyPhaseOffset * 1000000,
			expectedFrequencyStatus: func() int64 {
				if pinType == 2 {
					return 4
				} else {
					return 3
				}
			}(),
			expectedInSpecState: func() bool {
				if pinType == 2 {
					return false
				} else {
					return true
				}
			}(),
			desc: fmt.Sprintf("4.Out of holdover state.or stays in free run for pps , gns:=declare outofSpec Freerun within a sec : pin %d ", pinType),
		},
	}
}

func TestDpllConfig_MonitorProcessGNSS(t *testing.T) {
	dpll.MockDpllReplies = make(chan *nl.DoDeviceGetReply, 1)
	assert.True(t, dpll.MockDpllReplies != nil)
	eChannel := make(chan event.EventChannel, 10)
	closeChn := make(chan bool)
	// event has to be running before dpll is started
	eventProcessor := event.Init("node", false, "/tmp/go.sock", eChannel, closeChn, nil, nil, nil)
	d := dpll.NewDpll(clockid, 10, 2, 5, "ens01",
		[]event.EventSource{event.GNSS}, dpll.MOCK, map[string]map[string]string{}, 0, 0)
	d.CmdInit()
	eventChannel := make(chan event.EventChannel, 10)
	go eventProcessor.ProcessEvents()

	time.Sleep(5 * time.Second)
	if d != nil {
		d.MonitorProcess(config.ProcessConfig{
			ClockType:       "GM",
			ConfigName:      "test",
			EventChannel:    eventChannel,
			GMThreshold:     config.Threshold{},
			InitialPTPState: event.PTP_FREERUN,
		})
	}
	fmt.Println("starting Mock replies ")
	for _, tt := range getTestData(event.GNSS, 2) {
		d.SetSourceLost(tt.sourceLost)
		d.SetPhaseOffset(tt.expectedPhaseOffset)
		dpll.MockDpllReplies <- tt.reply
		time.Sleep(10 * time.Millisecond)
		d.SetDependsOn([]event.EventSource{tt.source})
		d.MonitorDpllMock()
		time.Sleep(10 * time.Millisecond)
		assert.Equal(t, tt.expectedIntermediateState, d.State(), tt.desc)
		time.Sleep(tt.sleep * time.Second)
		assert.Equal(t, tt.expectedPhaseStatus, d.PhaseStatus(), tt.desc)
		assert.Equal(t, tt.expectedFrequencyStatus, d.FrequencyStatus(), tt.desc)
		assert.Equal(t, tt.expectedPhaseOffset/1000000, d.PhaseOffset(), tt.desc)
		assert.Equal(t, tt.expectedState, d.State(), tt.desc)
		assert.Equal(t, tt.expectedInSpecState, d.InSpec())
	}
	closeChn <- true
}

func TestDpllConfig_MonitorProcessPPS(t *testing.T) {
	dpll.MockDpllReplies = make(chan *nl.DoDeviceGetReply, 1)
	assert.True(t, dpll.MockDpllReplies != nil)
	eChannel := make(chan event.EventChannel, 10)
	closeChn := make(chan bool)
	// event has to be running before dpll is started
	eventProcessor := event.Init("node", false, "/tmp/go.sock", eChannel, closeChn, nil, nil, nil)
	d := dpll.NewDpll(clockid, 10, 2, 5, "ens01",
		[]event.EventSource{event.GNSS}, dpll.MOCK, map[string]map[string]string{}, 0, 0)
	d.CmdInit()
	eventChannel := make(chan event.EventChannel, 10)
	go eventProcessor.ProcessEvents()

	time.Sleep(5 * time.Second)
	if d != nil {
		d.MonitorProcess(config.ProcessConfig{
			ClockType:       "GM",
			ConfigName:      "test",
			EventChannel:    eventChannel,
			GMThreshold:     config.Threshold{},
			InitialPTPState: event.PTP_FREERUN,
		})
	}
	fmt.Println("starting Mock replies ")
	for _, tt := range getTestData(event.PPS, 1) {
		d.SetSourceLost(tt.sourceLost)
		d.SetPhaseOffset(tt.expectedPhaseOffset)
		d.SetDependsOn([]event.EventSource{tt.source})
		dpll.MockDpllReplies <- tt.reply
		d.MonitorDpllMock()
		time.Sleep(tt.sleep * time.Second)
		assert.Equal(t, tt.expectedPhaseStatus, d.PhaseStatus(), tt.desc)
		assert.Equal(t, tt.expectedFrequencyStatus, d.FrequencyStatus(), tt.desc)
		assert.Equal(t, tt.expectedPhaseOffset/1000000, d.PhaseOffset(), tt.desc)
		assert.Equal(t, tt.expectedState, d.State(), tt.desc)
		assert.Equal(t, tt.expectedInSpecState, d.InSpec(), tt.desc)

	}
	closeChn <- true
}

func TestSysfs(t *testing.T) {
	//indexStr := fmt.Sprintf("/sys/class/net/%s/ifindex", "lo")
	//fContent, err := os.ReadFile(indexStr)
	//assert.Nil(t, err)
	fcontentStr := strings.ReplaceAll("-26644444444444444", "\n", "")
	index, err2 := strconv.ParseInt(fcontentStr, 10, 64)
	assert.Nil(t, err2)
	assert.GreaterOrEqual(t, index, int64(-26644444444444444))
}

type dpllTestCase struct {
	localMaxHoldoverOffSet uint64
	localHoldoverTimeout   uint64
	maxInSpecOffset        uint64
	expectedSlope          float64
	expectedTimeout        int64
}

func TestSlopeAndTimer(t *testing.T) {

	testCase := []dpllTestCase{
		{
			localMaxHoldoverOffSet: 6000,       // in ns
			localHoldoverTimeout:   100,        // in sec
			maxInSpecOffset:        100,        // in ns
			expectedSlope:          6000 / 100, // 60ns rate of change
			expectedTimeout:        2,          // 100/60 = 2 sec (maxInSpecOffset /slope)
		},
	}
	for _, tt := range testCase {
		d := dpll.NewDpll(100, tt.localMaxHoldoverOffSet, tt.localHoldoverTimeout, tt.maxInSpecOffset,
			"test", []event.EventSource{}, dpll.MOCK, map[string]map[string]string{}, 0, 0)
		assert.Equal(t, tt.localMaxHoldoverOffSet, d.LocalMaxHoldoverOffSet, "localMaxHoldover offset")
		assert.Equal(t, tt.localHoldoverTimeout, d.LocalHoldoverTimeout, "Local holdover timeout")
		assert.Equal(t, tt.maxInSpecOffset, d.MaxInSpecOffset, "Max In Spec Offset")
		assert.Equal(t, tt.expectedTimeout, d.Timer(), "Timer in secs")
		assert.Equal(t, tt.expectedSlope, d.Slope(), "Slope")
	}
}
