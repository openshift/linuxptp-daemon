package synce_test

import (
	"fmt"
	"github.com/openshift/linuxptp-daemon/pkg/synce"
	"github.com/stretchr/testify/assert"
	"k8s.io/utils/pointer"
	"testing"
)

type testCase struct {
	sms              byte
	smsExtended      byte
	enabledExtQl     int
	expectedPriority int
	expectedClock    string
	networkOption    int
}

type logTestCase struct {
	output             string
	enabledExtQl       bool
	expectedState      *string
	expectedDevice     *string
	expectedExtSource  *string
	expectedSource     *string
	expectedQL         byte
	expectedExtendedQL byte
}

var (
	logTestCases []logTestCase
	option1      = synce.GetQualityLevelInfoOption1()
	option2      = synce.GetQualityLevelInfoOption2()
)
var testCases = []testCase{
	{
		sms:              option2[synce.PRTC].SSM,
		smsExtended:      synce.QL_DEFAULT_ENHSSM,
		expectedPriority: option2[synce.PRTC].Priority,
		expectedClock:    synce.PRS.String(),
		networkOption:    synce.SYNCE_NETWORK_OPT_2,
		enabledExtQl:     0,
	},
	{
		sms:              option2[synce.EPRTC].SSM,
		smsExtended:      option2[synce.EPRTC].ExtendedSSM,
		expectedPriority: option2[synce.EPRTC].Priority,
		expectedClock:    synce.EPRTC.String(),
		networkOption:    2,
		enabledExtQl:     1,
	},
	{
		sms:              option1[synce.EPRTC].SSM,
		smsExtended:      option1[synce.EPRTC].ExtendedSSM,
		expectedPriority: option1[synce.EPRTC].Priority,
		expectedClock:    synce.EPRTC.String(),
		networkOption:    1,
		enabledExtQl:     1,
	},
	{
		sms:              option2[synce.PRS].SSM,
		smsExtended:      option2[synce.PRS].ExtendedSSM,
		expectedPriority: option2[synce.PRS].Priority,
		expectedClock:    synce.PRS.String(),
		networkOption:    2,
		enabledExtQl:     1,
	},
	{
		sms:              option2[synce.PRS].SSM,
		smsExtended:      option2[synce.PRS].ExtendedSSM,
		expectedPriority: option2[synce.PRS].Priority,
		expectedClock:    synce.DNU.String(),
		networkOption:    1,
		enabledExtQl:     1,
	},
	{
		sms:              option2[synce.PRS].SSM,
		smsExtended:      option2[synce.EEC1].ExtendedSSM,
		expectedPriority: 0,
		expectedClock:    synce.DUS.String(),
		networkOption:    2,
		enabledExtQl:     1,
	},
}

func Test_GetClockQuality(t *testing.T) {
	device := synce.Config{
		Name:           "synce1",
		Ifaces:         nil,
		ClockId:        "",
		NetworkOption:  synce.SYNCE_NETWORK_OPT_2,
		ExtendedTlv:    synce.ExtendedTLV_ENABLED,
		ExternalSource: "",
	}
	synce.PrintOption1Networks()
	synce.PrintOption2Networks()
	for _, tt := range testCases {
		device.NetworkOption = tt.networkOption
		device.ExtendedTlv = tt.enabledExtQl
		clock, _ := device.ClockQuality(synce.QualityLevelInfo{
			Priority:    0,
			SSM:         tt.sms,
			ExtendedSSM: tt.smsExtended,
		})
		assert.Equal(t, tt.expectedClock, clock)
	}

}

func TestParseLog(t *testing.T) {

	// Printing results
	InitLogTestCase()
	for _, l := range logTestCases {
		entry := synce.ParseLog(l.output)
		assert.Equal(t, l.expectedDevice, entry.Device, entry.String())
		assert.Equal(t, l.expectedExtSource, entry.ExtSource, entry.String())
		assert.Equal(t, l.expectedSource, entry.Source, entry.String())

	}
}

func InitLogTestCase() {
	logTestCases = []logTestCase{
		{
			output:             "synce4l[1225226.278]: [synce4l.0.config] tx_rebuild_tlv: attached new TLV, QL=0x1 on ens7f0",
			expectedState:      nil,
			expectedSource:     pointer.String("ens7f0"),
			expectedDevice:     nil,
			expectedQL:         option1[synce.PRTC].SSM,
			expectedExtendedQL: synce.QL_DEFAULT_SSM,
			enabledExtQl:       false,
		},
		{
			output:             fmt.Sprintf("synce4l[622796.479]: [synce4l.0.config] tx_rebuild_tlv: attached new TLV, QL=%#x on ens7f0", option1[synce.PRTC].SSM),
			expectedState:      nil,
			expectedSource:     pointer.String("ens7f0"),
			expectedDevice:     nil,
			expectedQL:         option1[synce.PRTC].SSM,
			expectedExtendedQL: synce.QL_DEFAULT_SSM,
			enabledExtQl:       false,
		},
		{
			output:             fmt.Sprintf("synce4l[622796.479]: [synce4l.0.config] tx_rebuild_tlv: attached new extended TLV, EXT_QL=%#x on ens7f0", option1[synce.PRTC].ExtendedSSM),
			expectedState:      nil,
			expectedDevice:     nil,
			expectedSource:     pointer.String("ens7f0"),
			expectedExtSource:  nil,
			expectedQL:         synce.QL_DEFAULT_SSM,
			expectedExtendedQL: 0xff,
			enabledExtQl:       true,
		},
		{
			output:             "synce4l[627602.540]: [synce4l.0.config]EEC_LOCKED/EEC_LOCKED_HO_ACQ on GNSS of synce1",
			expectedState:      pointer.String(synce.EEC_LOCKED.String()),
			expectedDevice:     pointer.String("synce1"),
			expectedExtSource:  pointer.String("GNSS"),
			expectedSource:     nil,
			expectedQL:         synce.QL_DEFAULT_SSM,
			expectedExtendedQL: synce.QL_DEFAULT_SSM,
		},
		{
			output:             "synce4l[627602.540]: [synce4l.0.config] EEC_HOLDOVER on synce1",
			expectedState:      pointer.String(synce.EEC_HOLDOVER.String()),
			expectedExtSource:  nil,
			expectedDevice:     pointer.String("synce1"),
			expectedSource:     nil,
			expectedQL:         synce.QL_DEFAULT_SSM,
			expectedExtendedQL: synce.QL_DEFAULT_SSM,
		},
		{
			output:             "synce4l[627602.593]: [synce4l.0.config] tx_rebuild_tlv: attached new TLV, QL=0x1 on ens7f0",
			expectedState:      nil,
			expectedDevice:     nil,
			expectedSource:     pointer.String("ens7f0"),
			expectedExtSource:  nil,
			expectedQL:         0x1,
			expectedExtendedQL: synce.QL_DEFAULT_SSM,
		},
		{
			output:             "synce4l[627602.593]: [synce4l.0.config] tx_rebuild_tlv: attached new extended TLV, EXT_QL=0xff on ens7f0",
			expectedState:      nil,
			expectedDevice:     nil,
			expectedSource:     pointer.String("ens7f0"),
			expectedExtSource:  nil,
			expectedQL:         synce.QL_DEFAULT_SSM,
			expectedExtendedQL: 0xff,
		},
		{
			output:             "synce4l[627685.138]: [synce4l.0.config] EEC_LOCKED/EEC_LOCKED_HO_ACQ on GNSS of synce1",
			expectedState:      pointer.String(synce.EEC_LOCKED_HO_ACQ.String()),
			expectedDevice:     pointer.String("synce1"),
			expectedExtSource:  pointer.String("GNSS"),
			expectedSource:     nil,
			expectedQL:         synce.QL_DEFAULT_SSM,
			expectedExtendedQL: synce.QL_DEFAULT_SSM,
		},
		{
			output:             "synce4l[627685.138]: [synce4l.0.config] act on EEC_LOCKED/EEC_LOCKED_HO_ACQ for ens7f0",
			expectedState:      pointer.String(synce.EEC_LOCKED_HO_ACQ.String()),
			expectedExtSource:  nil,
			expectedDevice:     nil,
			expectedSource:     pointer.String("ens7f0"),
			expectedQL:         synce.QL_DEFAULT_SSM,
			expectedExtendedQL: synce.QL_DEFAULT_SSM,
		},
	}

}
