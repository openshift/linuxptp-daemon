package daemon_test

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/synce"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/sirupsen/logrus"
	"k8s.io/utils/pointer"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/daemon"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	teardownTests := testhelpers.SetupTests()
	defer teardownTests()
	setup()
	code := m.Run()
	teardown()
	os.Exit(code)
}

func setup() {
	flag.Set("alsologtostderr", fmt.Sprintf("%t", true))
	var logLevel string
	flag.StringVar(&logLevel, "logLevel", "4", "test")
	flag.Lookup("v").Value.Set(logLevel)
	daemon.InitializeOffsetMaps()
	pm = daemon.NewProcessManager()
	daemon.RegisterMetrics(MYNODE)
}

func teardown() {
}

const (
	s0     = 0.0
	s1     = 2.0
	s2     = 1.0
	MYNODE = "mynode"
	// SKIP skip the verification of the metric
	SKIP    = 12345678
	CLEANUP = -12345678
)

var pm *daemon.ProcessManager
var registry *prometheus.Registry
var logTestCases []synceLogTestCase

type TestCase struct {
	MessageTag                  string
	Name                        string
	Ifaces                      config.IFaces
	log                         string
	from                        string
	process                     string
	node                        string
	iface                       string
	expectedOffset              float64 // offset_ns
	expectedMaxOffset           float64 // max_offset_ns1
	expectedFrequencyAdjustment float64 // frequency_adjustment_ns
	expectedDelay               float64 // delay_ns
	expectedClockState          float64 // clock_state
	expectedNmeaStatus          float64 // nmea_status
	expectedPpsStatus           float64 // pps_status
	expectedClockClassMetrics   float64 // clock_class
	expectedInterfaceRole       float64 // role
}

func (tc *TestCase) init() {
	tc.expectedOffset = SKIP
	tc.expectedMaxOffset = SKIP
	tc.expectedFrequencyAdjustment = SKIP
	tc.expectedDelay = SKIP
	tc.expectedClockState = SKIP
	tc.expectedNmeaStatus = SKIP
	tc.expectedPpsStatus = SKIP
	tc.expectedClockClassMetrics = SKIP
	tc.expectedInterfaceRole = SKIP
}

func (tc *TestCase) String() string {
	b := strings.Builder{}
	b.WriteString("log: \"" + tc.log + "\"\n")
	b.WriteString("from: " + tc.from + "\n")
	b.WriteString("process: " + tc.process + "\n")
	b.WriteString("node: " + tc.node + "\n")
	b.WriteString("iface: " + tc.iface + "\n")
	return b.String()
}

func (tc *TestCase) cleanupMetrics() {
	daemon.Offset.With(map[string]string{"from": tc.from, "process": tc.process, "node": tc.node, "iface": tc.iface}).Set(CLEANUP)
	daemon.MaxOffset.With(map[string]string{"from": tc.from, "process": tc.process, "node": tc.node, "iface": tc.iface}).Set(CLEANUP)
	daemon.FrequencyAdjustment.With(map[string]string{"from": tc.from, "process": tc.process, "node": tc.node, "iface": tc.iface}).Set(CLEANUP)
	daemon.Delay.With(map[string]string{"from": tc.from, "process": tc.process, "node": tc.node, "iface": tc.iface}).Set(CLEANUP)
	daemon.ClockState.With(map[string]string{"process": tc.process, "node": tc.node, "iface": tc.iface}).Set(CLEANUP)
	daemon.ClockClassMetrics.With(map[string]string{"process": tc.process, "node": tc.node}).Set(CLEANUP)
	daemon.InterfaceRole.With(map[string]string{"process": tc.process, "node": tc.node, "iface": tc.iface}).Set(CLEANUP)
}

var testCases = []TestCase{
	{
		log:                         "phc2sys[1823126.732]: [ptp4l.0.config] CLOCK_REALTIME phc offset       -10 s2 freq   +8956 delay    508",
		MessageTag:                  "[ptp4l.0.config]",
		Name:                        "phc2sys_basic_negative_offset_locked",
		from:                        "phc",
		process:                     "phc2sys",
		iface:                       "CLOCK_REALTIME",
		expectedOffset:              -10,
		expectedMaxOffset:           -10,
		expectedFrequencyAdjustment: 8956,
		expectedDelay:               508,
		expectedClockState:          s2,
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "ts2phc[1896327.319]: [ts2phc.0.config] ens2f0 master offset         -1 s2 freq      -2",
		MessageTag:                  "[ts2phc.0.config]",
		Name:                        "ts2phc_interface_negative_offset_locked",
		from:                        "master",
		process:                     "ts2phc",
		iface:                       "ens2fx",
		expectedOffset:              -1,
		expectedMaxOffset:           -1,
		expectedFrequencyAdjustment: -2,
		expectedDelay:               0,
		expectedClockState:          s2,
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "ts2phc[1896327.319]: [ts2phc.0.config] dev/ptp4  offset    -1 s2 freq      -2",
		MessageTag:                  "[ts2phc.0.config]",
		Name:                        "ts2phc_phc_device_negative_offset_locked",
		from:                        "master",
		process:                     "ts2phc",
		iface:                       "ens2fx",
		expectedOffset:              -1,
		expectedMaxOffset:           -1,
		expectedFrequencyAdjustment: -2,
		expectedDelay:               0,
		expectedClockState:          s2,
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
		Ifaces: []config.Iface{
			{
				Name:     "ens2fx",
				IsMaster: false,
				Source:   "",
				PhcId:    "dev/ptp4",
			},
		},
	},
	{
		log:                         "ts2phc[1896327.319]: [ts2phc.0.config] ens2f0 master offset         3 s0 freq      4",
		MessageTag:                  "[ts2phc.0.config]",
		Name:                        "ts2phc_positive_offset_freerun",
		from:                        "master",
		process:                     "ts2phc",
		iface:                       "ens2fx",
		expectedOffset:              3,
		expectedMaxOffset:           3,
		expectedFrequencyAdjustment: 4,
		expectedDelay:               0,
		expectedClockState:          s0,
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "ptp4l[8542280.698]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
		MessageTag:                  "[ptp4l.0.config]",
		Name:                        "ptp4l_uncalibrated_to_slave_transition",
		from:                        "master",
		process:                     "ptp4l",
		iface:                       "ens3f2",
		expectedOffset:              SKIP,
		expectedMaxOffset:           SKIP,
		expectedFrequencyAdjustment: SKIP,
		expectedDelay:               SKIP,
		expectedClockState:          SKIP,
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       1,
		Ifaces: []config.Iface{
			{
				Name:     "ens3f2",
				IsMaster: false,
				Source:   "",
				PhcId:    "phcid-2",
			},
		},
	},
	{
		log:                         "ptp4l[8537738.636]: [ptp4l.0.config] port 1: SLAVE to FAULTY on FAULT_DETECTED (FT_UNSPECIFIED)",
		MessageTag:                  "[ptp4l.0.config]",
		Name:                        "ptp4l_slave_to_faulty_fault_detection",
		from:                        "master",
		process:                     "ptp4l",
		iface:                       "ens3fx",
		expectedOffset:              999999, // faultyOffset
		expectedMaxOffset:           999999, // faultyOffset
		expectedFrequencyAdjustment: 0,
		expectedDelay:               0,
		expectedClockState:          s0, // FREERUN
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
		Ifaces: []config.Iface{
			{
				Name:     "ens3f2",
				IsMaster: false,
				Source:   "",
				PhcId:    "phcid-2",
			},
		},
	},
	// Additional test cases for extended coverage
	{
		log:                         "phc2sys[1823127.832]: [ptp4l.1.config] CLOCK_REALTIME phc offset       150 s0 freq   -12345 delay    1024",
		MessageTag:                  "[ptp4l.1.config]",
		Name:                        "phc2sys_positive_offset_freerun",
		from:                        "phc",
		process:                     "phc2sys",
		iface:                       "CLOCK_REALTIME",
		expectedOffset:              150,
		expectedMaxOffset:           150,
		expectedFrequencyAdjustment: -12345,
		expectedDelay:               1024,
		expectedClockState:          s0, // s0 servo state maps to 0.0
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "phc2sys[1823129.050]: [phc2sys.0.config] CLOCK_REALTIME phc offset       987 s2 freq   -54321 delay   2048",
		MessageTag:                  "[phc2sys.0.config]",
		Name:                        "phc2sys_different_message_tag_locked",
		from:                        "phc",
		process:                     "phc2sys",
		iface:                       "CLOCK_REALTIME",
		expectedOffset:              987,
		expectedMaxOffset:           987,
		expectedFrequencyAdjustment: -54321,
		expectedDelay:               2048,
		expectedClockState:          s2, // s2 servo state maps to 1.0
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "ts2phc[1896329.521]: [ts2phc.2.config] ens10f0 master offset         -5 s2 freq      -3",
		MessageTag:                  "[ts2phc.2.config]",
		Name:                        "ts2phc_small_negative_values_locked",
		from:                        "master",
		process:                     "ts2phc",
		iface:                       "ens10fx",
		expectedOffset:              -5,
		expectedMaxOffset:           -5,
		expectedFrequencyAdjustment: -3,
		expectedDelay:               0,
		expectedClockState:          s2, // s2 servo state maps to 1.0
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	// Additional comprehensive test cases
	{
		log:                         "phc2sys[1823130.100]: [ptp4l.3.config] CLOCK_REALTIME phc offset         0 s1 freq       0 delay      0",
		MessageTag:                  "[ptp4l.3.config]",
		Name:                        "phc2sys_perfect_sync_zeros_freerun",
		from:                        "phc",
		process:                     "phc2sys",
		iface:                       "CLOCK_REALTIME",
		expectedOffset:              0,
		expectedMaxOffset:           0,
		expectedFrequencyAdjustment: 0,
		expectedDelay:               0,
		expectedClockState:          s0, // s1 servo state maps to FREERUN = 0
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "phc2sys[1823131.200]: [phc2sys.1.config] CLOCK_REALTIME phc offset   -999999 s0 freq +999999 delay  65535",
		MessageTag:                  "[phc2sys.1.config]",
		Name:                        "phc2sys_extreme_boundary_values_freerun",
		from:                        "phc",
		process:                     "phc2sys",
		iface:                       "CLOCK_REALTIME",
		expectedOffset:              -999999,
		expectedMaxOffset:           -999999,
		expectedFrequencyAdjustment: 999999,
		expectedDelay:               65535,
		expectedClockState:          s0, // s0 servo state
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "ts2phc[1896331.723]: [ts2phc.4.config] ens15f1 master offset     99999 s0 freq +500000",
		MessageTag:                  "[ts2phc.4.config]",
		Name:                        "ts2phc_large_positive_offset_freerun",
		from:                        "master",
		process:                     "ts2phc",
		iface:                       "ens15fx",
		expectedOffset:              99999,
		expectedMaxOffset:           99999,
		expectedFrequencyAdjustment: 500000,
		expectedDelay:               0,
		expectedClockState:          s0, // s0 servo state
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "ts2phc[1896332.824]: [ts2phc.5.config] dev/ptp12 offset        777 s1 freq   -88888",
		MessageTag:                  "[ts2phc.5.config]",
		Name:                        "ts2phc_phc_device_freerun",
		from:                        "master",
		process:                     "ts2phc",
		iface:                       "ens20fx",
		expectedOffset:              777,
		expectedMaxOffset:           777,
		expectedFrequencyAdjustment: -88888,
		expectedDelay:               0,
		expectedClockState:          s0, // s1 servo state maps to FREERUN = 0
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
		Ifaces: []config.Iface{
			{
				Name:     "ens20fx",
				IsMaster: false,
				Source:   "",
				PhcId:    "dev/ptp12",
			},
		},
	},
	{
		log:                         "ptp4l[8542282.900]: [ptp4l.2.config] port 1: MASTER to PASSIVE on RS_PASSIVE",
		MessageTag:                  "[ptp4l.2.config]",
		Name:                        "ptp4l_master_to_passive_transition",
		from:                        "master",
		process:                     "ptp4l",
		iface:                       "ens25f0",
		expectedOffset:              SKIP,
		expectedMaxOffset:           SKIP,
		expectedFrequencyAdjustment: SKIP,
		expectedDelay:               SKIP,
		expectedClockState:          SKIP,
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       0, // PASSIVE = 0
		Ifaces: []config.Iface{
			{
				Name:     "ens25f0",
				IsMaster: false,
				Source:   "",
				PhcId:    "phcid-25",
			},
		},
	},
	{
		log:                         "ptp4l[8542283.101]: [ptp4l.3.config] port 1: LISTENING to MASTER on RS_MASTER",
		MessageTag:                  "[ptp4l.3.config]",
		Name:                        "ptp4l_listening_to_master_transition",
		from:                        "master",
		process:                     "ptp4l",
		iface:                       "ens30f1",
		expectedOffset:              SKIP,
		expectedMaxOffset:           SKIP,
		expectedFrequencyAdjustment: SKIP,
		expectedDelay:               SKIP,
		expectedClockState:          SKIP,
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       2, // MASTER = 2
		Ifaces: []config.Iface{
			{
				Name:     "ens30f1",
				IsMaster: true,
				Source:   "",
				PhcId:    "phcid-30",
			},
		},
	},
	{
		log:                         "ptp4l[412707.219]: [ptp4l.0.config:5] port 1 (ens8f2): LISTENING to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES",
		MessageTag:                  "[ptp4l.0.config:5]",
		Name:                        "ptp4l_listening_to_master_transition_with_port_name",
		from:                        "master",
		process:                     "ptp4l",
		iface:                       "ens8f2",
		expectedOffset:              SKIP,
		expectedMaxOffset:           SKIP,
		expectedFrequencyAdjustment: SKIP,
		expectedDelay:               SKIP,
		expectedClockState:          SKIP,
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       2, // MASTER = 2
		Ifaces: []config.Iface{
			{
				Name:     "ens8f2",
				IsMaster: false,
				Source:   "",
				PhcId:    "phcid-25",
			},
		},
	},
	{
		log:                         "ptp4l[8542284.202]: [ptp4l.4.config] port 1: INITIALIZING to LISTENING on INITIALIZE_COMPLETE",
		MessageTag:                  "[ptp4l.4.config]",
		Name:                        "ptp4l_initializing_to_listening_transition",
		from:                        "master",
		process:                     "ptp4l",
		iface:                       "ens35f0",
		expectedOffset:              SKIP,
		expectedMaxOffset:           SKIP,
		expectedFrequencyAdjustment: SKIP,
		expectedDelay:               SKIP,
		expectedClockState:          SKIP,
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       5, // LISTENING = 5
		Ifaces: []config.Iface{
			{
				Name:     "ens35f0",
				IsMaster: false,
				Source:   "",
				PhcId:    "phcid-35",
			},
		},
	},
	{
		log:                         "phc2sys[1823132.300]: [ptp4l.5.config] CLOCK_REALTIME phc offset     -500000 s2 freq  -250000 delay    999",
		MessageTag:                  "[ptp4l.5.config]",
		Name:                        "phc2sys_large_negative_values_locked",
		from:                        "phc",
		process:                     "phc2sys",
		iface:                       "CLOCK_REALTIME",
		expectedOffset:              -500000,
		expectedMaxOffset:           -500000,
		expectedFrequencyAdjustment: -250000,
		expectedDelay:               999,
		expectedClockState:          s2, // s2 servo state maps to LOCKED = 1
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "ts2phc[1896333.925]: [ts2phc.6.config] ens40f0 master offset      1234 s2 freq    -5678",
		MessageTag:                  "[ts2phc.6.config]",
		Name:                        "ts2phc_positive_values_locked",
		from:                        "master",
		process:                     "ts2phc",
		iface:                       "ens40fx",
		expectedOffset:              1234,
		expectedMaxOffset:           1234,
		expectedFrequencyAdjustment: -5678,
		expectedDelay:               0,
		expectedClockState:          s2, // s2 servo state maps to LOCKED = 1
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "phc2sys[1823133.400]: [phc2sys.2.config] CLOCK_REALTIME phc offset        42 s1 freq      +1 delay      1",
		MessageTag:                  "[phc2sys.2.config]",
		Name:                        "phc2sys_small_positive_values_freerun",
		from:                        "phc",
		process:                     "phc2sys",
		iface:                       "CLOCK_REALTIME",
		expectedOffset:              42,
		expectedMaxOffset:           42,
		expectedFrequencyAdjustment: 1,
		expectedDelay:               1,
		expectedClockState:          s0, // s1 servo state maps to FREERUN = 0
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
	},
	{
		log:                         "ts2phc[1896334.026]: [ts2phc.7.config] dev/ptp99 offset       -1 s2 freq      +1",
		MessageTag:                  "[ts2phc.7.config]",
		Name:                        "ts2phc_minimal_values_phc_device_locked",
		from:                        "master",
		process:                     "ts2phc",
		iface:                       "ens99fx",
		expectedOffset:              -1,
		expectedMaxOffset:           -1,
		expectedFrequencyAdjustment: 1,
		expectedDelay:               0,
		expectedClockState:          s2, // s2 servo state maps to LOCKED = 1
		expectedNmeaStatus:          SKIP,
		expectedPpsStatus:           SKIP,
		expectedClockClassMetrics:   SKIP,
		expectedInterfaceRole:       SKIP,
		Ifaces: []config.Iface{
			{
				Name:     "ens99fx",
				IsMaster: false,
				Source:   "",
				PhcId:    "dev/ptp99",
			},
		},
	},
}

func Test_ProcessPTPMetrics(t *testing.T) {
	skip, teardownTest := testhelpers.SetupForTestGM()
	defer teardownTest()
	if skip {
		t.Skip("GM is not supported")
		t.SkipNow()
	}

	leap.MockLeapFile()
	defer func() {
		close(leap.LeapMgr.Close)
		// Sleep to allow context to switch
		time.Sleep(100 * time.Millisecond)
		assert.Nil(t, leap.LeapMgr)
	}()

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			assert := assert.New(t)
			tc.node = MYNODE
			tc.cleanupMetrics()
			pm.SetTestData(tc.process, tc.MessageTag, tc.Ifaces)
			pm.RunProcessPTPMetrics(tc.log)

			if tc.expectedOffset != SKIP {
				ptpOffset := daemon.Offset.With(map[string]string{"from": tc.from, "process": tc.process, "node": tc.node, "iface": tc.iface})
				assert.Equal(tc.expectedOffset, testutil.ToFloat64(ptpOffset), "Offset does not match\n%s", tc.String())
			}
			if tc.expectedMaxOffset != SKIP {
				ptpMaxOffset := daemon.MaxOffset.With(map[string]string{"from": tc.from, "process": tc.process, "node": tc.node, "iface": tc.iface})
				assert.Equal(tc.expectedMaxOffset, testutil.ToFloat64(ptpMaxOffset), "MaxOffset does not match\n%s", tc.String())
			}
			if tc.expectedFrequencyAdjustment != SKIP {
				ptpFrequencyAdjustment := daemon.FrequencyAdjustment.With(map[string]string{"from": tc.from, "process": tc.process, "node": tc.node, "iface": tc.iface})
				assert.Equal(tc.expectedFrequencyAdjustment, testutil.ToFloat64(ptpFrequencyAdjustment), "FrequencyAdjustment does not match\n%s", tc.String())
			}
			if tc.expectedDelay != SKIP {
				ptpDelay := daemon.Delay.With(map[string]string{"from": tc.from, "process": tc.process, "node": tc.node, "iface": tc.iface})
				assert.Equal(tc.expectedDelay, testutil.ToFloat64(ptpDelay), "Delay does not match\n%s", tc.String())
			}
			if tc.expectedClockState != SKIP {
				clockState := daemon.ClockState.With(map[string]string{"process": tc.process, "node": tc.node, "iface": tc.iface})
				assert.Equal(tc.expectedClockState, testutil.ToFloat64(clockState), "ClockState does not match\n%s", tc.String())
			}
			if tc.expectedClockClassMetrics != SKIP {
				clockClassMetrics := daemon.ClockClassMetrics.With(map[string]string{"process": tc.process, "node": tc.node})
				assert.Equal(tc.expectedClockClassMetrics, testutil.ToFloat64(clockClassMetrics), "ClockClassMetrics does not match\n%s", tc.String())
			}
			if tc.expectedInterfaceRole != SKIP {
				role := daemon.InterfaceRole.With(map[string]string{"process": tc.process, "node": tc.node, "iface": tc.iface})
				assert.Equal(tc.expectedInterfaceRole, testutil.ToFloat64(role), "InterfaceRole does not match\n%s", tc.String())
			}
		})
	}
}

func TestDaemon_ApplyHaProfiles(t *testing.T) {
	skip, teardownTest := testhelpers.SetupForTestBCPTPHA()
	defer teardownTest()
	if skip {
		t.Skip("BC PTP-HA is not supported")
		t.SkipNow()
	}

	p1 := ptpv1.PtpProfile{
		Name: pointer.String("profile1"),
	}
	p2 := ptpv1.PtpProfile{
		Name: pointer.String("profile2"),
	}
	p3 := ptpv1.PtpProfile{
		Name:        pointer.String("ha_profile1"),
		PtpSettings: map[string]string{daemon.PTP_HA_IDENTIFIER: "profile1,profile2"},
	}
	processManager := daemon.NewProcessManager()
	ifaces1 := []config.Iface{
		{
			Name:     "ens2f2",
			IsMaster: false,
			Source:   "",
			PhcId:    "phcid-2",
		},
	}
	ifaces2 := []config.Iface{
		{
			Name:     "ens3f2",
			IsMaster: false,
			Source:   "",
			PhcId:    "phcid-2",
		},
	}

	processManager.SetTestProfileProcess(*p1.Name, ifaces1, "socket1", "config1", p1)
	processManager.SetTestProfileProcess(*p2.Name, ifaces2, "socket2", "config1", p2)
	processManager.SetTestProfileProcess(*p3.Name, nil, "", "config1", p3)
	dd := daemon.NewDaemonForTests(&daemon.ReadyTracker{}, processManager)
	haProfiles, cmdLine := dd.ApplyHaProfiles(&p3, "")
	assert.NotEmpty(t, cmdLine, "cmdLine is not empty")
	assert.Equal(t, len(haProfiles), 2, "ha has two profiles")
	assert.Equal(t, cmdLine, "-z socket1 -z socket2", "CmdLine is empty")
}

var (
	option1 = synce.GetQualityLevelInfoOption1()
	option2 = synce.GetQualityLevelInfoOption2()
)

type synceLogTestCase struct {
	output               string
	expectedState        event.PTPState
	expectedDevice       *string
	expectedSource       *string
	expectedClockQuality string
	expectedQL           byte
	expectedExtendedQL   byte
	extendedTvl          int
	networkOption        int
	expectedDescription  string
}

func InitSynceLogTestCase() {
	logTestCases = []synceLogTestCase{

		{
			output:               "synce4l[1225226.279]: [synce4l.0.config] tx_rebuild_tlv: attached new TLV, QL=0x1 on ens7f0",
			expectedState:        "",
			expectedDevice:       pointer.String("synce1"),
			expectedQL:           option2[synce.PRTC].SSM,
			expectedExtendedQL:   synce.QL_DEFAULT_SSM, //**If extended SSM is not enabled, it's implicitly assumed as 0xFF
			expectedSource:       pointer.String("ens7f0"),
			extendedTvl:          1,
			networkOption:        2,
			expectedClockQuality: "",
			expectedDescription:  " initial state with nPRTCo ext_ql should set ClockQuality for networkOption1 ",
		},

		{
			output:               fmt.Sprintf("synce4l[622796.479]: [synce4l.0.config] tx_rebuild_tlv: attached new TLV, QL=%#x on ens7f0", option1[synce.PRC].SSM),
			expectedState:        "",
			expectedDevice:       pointer.String("synce1"),
			expectedQL:           option1[synce.PRC].SSM,
			expectedExtendedQL:   synce.QL_DEFAULT_ENHSSM, //**If extended SSM is not enabled, it's implicitly assumed as 0xFF
			expectedSource:       pointer.String("ens7f0"),
			extendedTvl:          0,
			networkOption:        1,
			expectedClockQuality: synce.PRC.String(),
			expectedDescription:  " initial state with no ext_ql should set ClockQuality for networkOption1 ",
		},
		{
			output:               fmt.Sprintf("synce4l[622796.479]: [synce4l.0.config] tx_rebuild_tlv: attached new extended TLV, EXT_QL=%#x on ens7f0", option1[synce.PRTC].ExtendedSSM),
			expectedState:        "",
			expectedDevice:       pointer.String("synce1"),
			expectedSource:       pointer.String("ens7f0"),
			expectedQL:           option1[synce.PRTC].SSM,
			expectedExtendedQL:   option1[synce.PRTC].ExtendedSSM,
			extendedTvl:          1,
			networkOption:        1,
			expectedClockQuality: synce.PRTC.String(),
			expectedDescription:  "With extended QL enabled for network option 2 but have previously captured QL for network option 1,the Clock quality should be reported  ",
		},
		{
			output:               fmt.Sprintf("synce4l[622796.479]: [synce4l.0.config] tx_rebuild_tlv: attached new TLV, QL=%#x on ens7f0", option2[synce.PRTC].SSM),
			expectedState:        "",
			expectedDevice:       pointer.String("synce1"),
			expectedSource:       pointer.String("ens7f0"),
			expectedQL:           option2[synce.PRTC].SSM,
			expectedExtendedQL:   option1[synce.PRTC].ExtendedSSM, //**If extended SSM is not enabled, it's implicitly assumed as 0xFF
			extendedTvl:          1,
			networkOption:        2,
			expectedClockQuality: synce.PRTC.String(),
			expectedDescription:  " initial state with no ext_ql should set ClockQuality for networkOption1 ",
		},
		{
			output:               fmt.Sprintf("synce4l[622796.479]: [synce4l.0.config] tx_rebuild_tlv: attached new extended TLV, EXT_QL=%#x on ens7f0", option1[synce.PRTC].ExtendedSSM),
			expectedState:        "",
			expectedDevice:       pointer.String("synce1"),
			expectedSource:       pointer.String("ens7f0"),
			expectedQL:           option2[synce.PRTC].SSM,
			expectedExtendedQL:   option2[synce.PRTC].ExtendedSSM,
			extendedTvl:          1,
			networkOption:        2,
			expectedClockQuality: synce.PRTC.String(),
			expectedDescription:  " Extended tlv enabled should not report clock quality yet for just QL ",
		},
		{
			output:               "synce4l[627602.540]: [synce4l.0.config] EEC_LOCKED/EEC_LOCKED_HO_ACQ for ens7f0",
			expectedState:        event.PTP_LOCKED,
			expectedDevice:       pointer.String("synce1"),
			expectedSource:       nil,
			expectedQL:           option2[synce.PRTC].SSM,
			expectedExtendedQL:   option2[synce.PRTC].ExtendedSSM,
			networkOption:        2,
			extendedTvl:          1,
			expectedClockQuality: synce.PRTC.String(),
			expectedDescription:  "LOCKED ",
		},
		{
			output:               "synce4l[627602.540]: [synce4l.0.config] EEC_LOCKED/EEC_LOCKED_HO_ACQ on synce1",
			expectedState:        event.PTP_LOCKED,
			expectedDevice:       pointer.String("synce1"),
			expectedSource:       nil,
			expectedQL:           option2[synce.PRTC].SSM,
			expectedExtendedQL:   option2[synce.PRTC].ExtendedSSM,
			networkOption:        2,
			extendedTvl:          1,
			expectedClockQuality: synce.PRTC.String(),
			expectedDescription:  "Will not produce any output;will use last data ",
		},
	}

}

func TestDaemon_ProcessSynceLogs(t *testing.T) {
	// Printing results
	InitSynceLogTestCase()
	skip, teardownTest := testhelpers.SetupForTestGMSyncE()
	defer teardownTest()
	if skip {
		t.Skip("SyncE is not enabled")
		t.SkipNow()
	}

	pm = daemon.NewProcessManager()
	relations := synce.Relations{Devices: make([]*synce.Config, 1)}
	relations.Devices[0] = &synce.Config{
		Name:           "synce1",
		Ifaces:         []string{"ens7f0"},
		ClockId:        "1",
		NetworkOption:  synce.SYNCE_NETWORK_OPT_1,
		ExtendedTlv:    synce.ExtendedTLV_DISABLED,
		ExternalSource: "",
		LastQLState:    make(map[string]*synce.QualityLevelInfo),
		LastClockState: "",
	}
	pm.UpdateSynceConfig(&relations)

	for _, l := range logTestCases {
		relations.Devices[0].NetworkOption = l.networkOption
		relations.Devices[0].ExtendedTlv = l.extendedTvl

		pm.RunSynceParser(l.output)
		if l.expectedSource != nil {
			cq, _ := relations.Devices[0].ClockQuality(*relations.Devices[0].LastQLState[*l.expectedSource])
			actualQuality := relations.Devices[0].LastQLState[*l.expectedSource]
			logrus.Infof("%s last QL %#x last ExtQL %#x ClockQuality %v state %s", l.output, actualQuality.SSM, actualQuality.ExtendedSSM, cq, relations.Devices[0].LastClockState)
			assert.Equal(t, l.expectedClockQuality, cq, l.output)
			assert.Equal(t, l.expectedState, relations.Devices[0].LastClockState, l.output)
			assert.Equal(t, l.expectedQL, actualQuality.SSM, l.output)
			assert.Equal(t, l.expectedExtendedQL, actualQuality.ExtendedSSM, l.output)
		}

		assert.Equal(t, *l.expectedDevice, relations.Devices[0].Name, l.output)
		assert.Equal(t, l.expectedState, relations.Devices[0].LastClockState, l.output)

	}
}

type populateAndRenderPtp4lConfTestCase struct {
	profileName    string
	testConf       string
	expectedOutput string
	testName       string
	iface          string
	messageTag     string
	socketPath     string
	pProcess       string
}

func initPopulateAndRenderPtp4lConfTestCase() []populateAndRenderPtp4lConfTestCase {
	ret := []populateAndRenderPtp4lConfTestCase{
		{
			profileName:    "OC",
			testConf:       "[global]\nslaveOnly 1",
			expectedOutput: "#profile: OC\n\n[global]\nslaveOnly 1\nmessage_tag test message_tag\nuds_address /var/run/ptp4l.0.socket\n[ens2f0]",
			testName:       "Basic OC test",
			iface:          "ens2f0",
			messageTag:     "test message_tag",
			socketPath:     "/var/run/ptp4l.0.socket",
			pProcess:       "",
		},
		{
			profileName:    "BC",
			testConf:       "[global]\nslaveOnly 0\n[ens1f0]\nmasterOnly 0\n[ens1f1]\nmasterOnly 1",
			expectedOutput: "#profile: BC\n\n[global]\nslaveOnly 0\nmessage_tag test message_tag\nuds_address /var/run/ptp4l.0.socket\n[ens1f0]\nmasterOnly 0\n[ens1f1]\nmasterOnly 1",
			testName:       "Basic BC test",
			iface:          "",
			messageTag:     "test message_tag",
			socketPath:     "/var/run/ptp4l.0.socket",
			pProcess:       "",
		},
		{
			profileName:    "GM",
			testConf:       "[global]\nslaveOnly 0\n[ens1f0]\nmasterOnly 1\n[ens1f1]\nmasterOnly 1",
			expectedOutput: "#profile: GM\n\n[global]\nslaveOnly 0\nmessage_tag test message_tag\nuds_address /var/run/ptp4l.0.socket\n[ens1f0]\nmasterOnly 1\n[ens1f1]\nmasterOnly 1",
			testName:       "Basic GM test",
			iface:          "",
			messageTag:     "test message_tag",
			socketPath:     "/var/run/ptp4l.0.socket",
			pProcess:       "",
		},
		{
			profileName:    "SyncE",
			testConf:       "[global]\nlogging_level 7\nuse_syslog 0\nverbose 1\n[<synce1>]\ndnu_prio 0xFF\nnetwork_option 2\nextended_tlv 1\nrecover_time 60\nclock_id\nmodule_name ice\n[enp59s0f0np0]\n[{SMA1}]\nboard_label SMA1\ninput_QL 0x1",
			expectedOutput: "#profile: SyncE\n\n[global]\nlogging_level 7\nuse_syslog 0\nverbose 1\nmessage_tag test message_tag\nuds_address /var/run/ptp4l.0.socket\n[<synce1>]\ndnu_prio 0xFF\nnetwork_option 2\nextended_tlv 1\nrecover_time 60\nmodule_name ice\n[enp59s0f0np0]\n[{SMA1}]\nboard_label SMA1\ninput_QL 0x1",
			testName:       "Basic SyncE test",
			iface:          "",
			messageTag:     "test message_tag",
			socketPath:     "/var/run/ptp4l.0.socket",
			pProcess:       "",
		},
	}
	return ret
}

func TestDaemon_PopulateAndRenderPtp4lConf(t *testing.T) {
	testCases := initPopulateAndRenderPtp4lConfTestCase()
	for _, tc := range testCases {
		conf := &daemon.Ptp4lConf{}
		conf.PopulatePtp4lConf(&tc.testConf)
		if tc.iface != "" {
			conf.AddInterfaceSection(tc.iface)
		}
		conf.ExtendGlobalSection(tc.profileName, tc.messageTag, tc.socketPath, tc.pProcess)
		actualOutput, _ := conf.RenderPtp4lConf()
		assert.Equal(t, tc.expectedOutput, actualOutput, fmt.Sprintf("Rendered output doesn't match expected: %s", tc.testName))
	}
}
