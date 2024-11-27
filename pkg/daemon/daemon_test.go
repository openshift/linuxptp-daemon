package daemon_test

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/josephdrichard/linuxptp-daemon/pkg/event"
	"github.com/josephdrichard/linuxptp-daemon/pkg/leap"
	"github.com/josephdrichard/linuxptp-daemon/pkg/synce"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/sirupsen/logrus"
	"k8s.io/utils/pointer"

	"github.com/josephdrichard/linuxptp-daemon/pkg/config"
	"github.com/josephdrichard/linuxptp-daemon/pkg/daemon"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

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
		Name:                        "phc2sys",
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
		Name:                        "ts2phc",
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
		Name:                        "ts2phc",
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
		Name:                        "ts2phc",
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
		Name:                        "ptp4l",
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
		Name:                        "ptp4l",
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

func TestMain(m *testing.M) {
	setup()
	code := m.Run()
	teardown()
	os.Exit(code)
}
func Test_ProcessPTPMetrics(t *testing.T) {
	leap.MockLeapFile()
	defer close(leap.LeapMgr.Close)

	assert := assert.New(t)
	for _, tc := range testCases {
		tc.node = MYNODE
		tc.cleanupMetrics()
		pm.SetTestData(tc.Name, tc.MessageTag, tc.Ifaces)
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
	}
}

func TestDaemon_ApplyHaProfiles(t *testing.T) {
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
	dd := &daemon.Daemon{}
	dd.SetProcessManager(processManager)
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
