package dpll_netlink

import (
	"math"
	"os"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/testhelpers"
	"github.com/mdlayher/genetlink"
	"github.com/mdlayher/netlink"
	"github.com/stretchr/testify/assert"
)

func TestMain(m *testing.M) {
	teardownTests := testhelpers.SetupTests()
	defer teardownTests()
	os.Exit(m.Run())
}

// test YNL encoding of the DPLL pin control API
// This tests three basic API use cases:
// - Phase adjustment
// - Input control through pin priority setting
// - Output control through through state setting
func Test_EncodePinControl(t *testing.T) {
	assert.New(t)
	// test phase adjustment
	pc := PinParentDeviceCtl{
		ID: 88,
		PhaseAdjust: func() *int32 {
			t := int32(math.MinInt32)
			return &t
		}(),
	}
	b, err := EncodePinControl(pc)
	assert.NoError(t, err, "failed to encode phase adjustment")
	expected, err := os.ReadFile("testdata/encoded-phase-adjustment")
	assert.NoError(t, err, "failed to read testdata for phase adjustment")
	assert.Equal(t, expected, b, "encoded data is different from the desired phase adjustment data")

	// Test priority settings
	pc.PhaseAdjust = nil
	pc.PinParentCtl = []PinControl{
		{
			PinParentID: 8,
			Prio: func() *uint32 {
				t := uint32(math.MaxUint32)
				return &t
			}(),
		},
		{
			PinParentID: 8,
			Prio: func() *uint32 {
				t := uint32(math.MaxUint8)
				return &t
			}(),
		},
	}
	b, err = EncodePinControl(pc)
	assert.NoError(t, err, "failed to encode pin priority setting")
	expected, err = os.ReadFile("testdata/encoded-prio")
	assert.NoError(t, err, "failed to read testdata for priority setting")
	assert.Equal(t, expected, b, "encoded data is different from the desired pin priority data")

	// Test setting the connection state
	pc.PinParentCtl = []PinControl{
		{
			PinParentID: 0,
			Prio:        nil,
			Direction: func() *uint32 {
				t := uint32(2)
				return &t
			}(),
		},
		{
			PinParentID: 1,
			Prio:        nil,
			Direction: func() *uint32 {
				t := uint32(1)
				return &t
			}(),
		},
	}
	b, err = EncodePinControl(pc)
	assert.NoError(t, err, "failed to encode pin connection setting")
	expected, err = os.ReadFile("testdata/encoded-connection")
	assert.NoError(t, err, "failed to read testdata for connection setting")
	assert.Equal(t, expected, b, "encoded data is different from the desired pin connection setting data")

}

// TestParsePinReplies_DpllPinFractionalFrequencyOffsetPPT verifies that
// ParsePinReplies correctly decodes the DpllPinFractionalFrequencyOffsetPPT
// attribute (FFO in parts per trillion) into PinInfo.FractionalFrequencyOffsetPPT.
func TestParsePinReplies_DpllPinFractionalFrequencyOffsetPPT(t *testing.T) {
	tests := []struct {
		name   string
		ffoPPT int32
	}{
		{"positive value", 12345},
		{"zero", 0},
		{"negative value", -999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := netlink.NewAttributeEncoder()
			ae.Uint32(DpllPinID, 1)
			ae.Int32(DpllPinFractionalFrequencyOffsetPPT, tt.ffoPPT)
			payload, err := ae.Encode()
			assert.NoError(t, err, "encode attributes")

			msgs := []genetlink.Message{{Data: payload}}
			replies, err := ParsePinReplies(msgs)
			assert.NoError(t, err)
			assert.Len(t, replies, 1)
			assert.Equal(t, int(tt.ffoPPT), replies[0].FractionalFrequencyOffsetPPT,
				"FractionalFrequencyOffsetPPT should match encoded value")
		})
	}
}

// TestGetFrequencyMonitor verifies that GetFrequencyMonitor maps kernel feature-state values
// to their canonical string representations.
func TestGetFrequencyMonitor(t *testing.T) {
	tests := []struct {
		name  string
		input uint32
		want  string
	}{
		{"enabled", PhaseOffsetMonitorEnabled, featureStateEnabled},
		{"disabled", PhaseOffsetMonitorDisabled, featureStateDisabled},
		{unknownStr, 99, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, GetFrequencyMonitor(tt.input))
		})
	}
}

// TestGetPinOperstate verifies that GetPinOperstate maps kernel pin-operstate values
// to their canonical string representations.
func TestGetPinOperstate(t *testing.T) {
	tests := []struct {
		name  string
		input uint32
		want  string
	}{
		{"active", PinOperstateActive, pinOperstateActive},
		{"standby", PinOperstateStandby, pinOperstateStandby},
		{"no-signal", PinOperstateNoSignal, pinOperstateNoSignal},
		{"qual-failed", PinOperstateQualFailed, pinOperstateQualFailed},
		{unknownStr, 99, unknownStr},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, GetPinOperstate(tt.input))
		})
	}
}

// TestParseDeviceReplies_FrequencyMonitor verifies that ParseDeviceReplies
// correctly decodes the DpllFrequencyMonitor attribute into DoDeviceGetReply.FrequencyMonitor.
func TestParseDeviceReplies_FrequencyMonitor(t *testing.T) {
	tests := []struct {
		name    string
		encoded uint32
		want    uint32
	}{
		{"enabled", PhaseOffsetMonitorEnabled, PhaseOffsetMonitorEnabled},
		{"disabled", PhaseOffsetMonitorDisabled, PhaseOffsetMonitorDisabled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := netlink.NewAttributeEncoder()
			ae.Uint32(DpllID, 1)
			ae.Uint32(DpllFrequencyMonitor, tt.encoded)
			payload, err := ae.Encode()
			assert.NoError(t, err)

			msgs := []genetlink.Message{{Data: payload}}
			replies, err := ParseDeviceReplies(msgs)
			assert.NoError(t, err)
			assert.Len(t, replies, 1)
			assert.Equal(t, tt.want, replies[0].FrequencyMonitor)
		})
	}
}

// TestParsePinReplies_MeasuredFrequency verifies that ParsePinReplies correctly
// decodes the DpllPinMeasuredFrequency attribute into PinInfo.MeasuredFrequency.
func TestParsePinReplies_MeasuredFrequency(t *testing.T) {
	tests := []struct {
		name  string
		mfreq uint64
	}{
		{"10 MHz expressed as millihertz", 10_000_000_000},
		{"zero", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := netlink.NewAttributeEncoder()
			ae.Uint32(DpllPinID, 1)
			ae.Uint64(DpllPinMeasuredFrequency, tt.mfreq)
			payload, err := ae.Encode()
			assert.NoError(t, err)

			msgs := []genetlink.Message{{Data: payload}}
			replies, err := ParsePinReplies(msgs)
			assert.NoError(t, err)
			assert.Len(t, replies, 1)
			assert.Equal(t, tt.mfreq, replies[0].MeasuredFrequency)
		})
	}
}

// TestParsePinReplies_Operstate verifies that ParsePinReplies correctly
// decodes the top-level DpllPinOperstate attribute into PinInfo.Operstate.
func TestParsePinReplies_Operstate(t *testing.T) {
	tests := []struct {
		name      string
		operstate uint32
	}{
		{"active", PinOperstateActive},
		{"standby", PinOperstateStandby},
		{"no-signal", PinOperstateNoSignal},
		{"qual-failed", PinOperstateQualFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := netlink.NewAttributeEncoder()
			ae.Uint32(DpllPinID, 1)
			ae.Uint32(DpllPinOperstate, tt.operstate)
			payload, err := ae.Encode()
			assert.NoError(t, err)

			msgs := []genetlink.Message{{Data: payload}}
			replies, err := ParsePinReplies(msgs)
			assert.NoError(t, err)
			assert.Len(t, replies, 1)
			assert.Equal(t, tt.operstate, replies[0].Operstate)
		})
	}
}

// TestParsePinReplies_ParentDeviceNewFields verifies that ParsePinReplies correctly
// decodes operstate, FFO, and FFO-PPT from the pin-parent-device nest.
func TestParsePinReplies_ParentDeviceNewFields(t *testing.T) {
	const parentID = uint32(7)
	const operstate = uint32(PinOperstateActive)
	const ffo = int32(-42)
	const ffoPPT = int32(12345)

	ae := netlink.NewAttributeEncoder()
	ae.Uint32(DpllPinID, 1)
	ae.Nested(DpllPinParentDevice, func(nae *netlink.AttributeEncoder) error {
		nae.Uint32(DpllPinParentID, parentID)
		nae.Uint32(DpllPinOperstate, operstate)
		nae.Int32(DpllPinFractionalFrequencyOffset, ffo)
		nae.Int32(DpllPinFractionalFrequencyOffsetPPT, ffoPPT)
		return nil
	})
	payload, err := ae.Encode()
	assert.NoError(t, err)

	msgs := []genetlink.Message{{Data: payload}}
	replies, err := ParsePinReplies(msgs)
	assert.NoError(t, err)
	assert.Len(t, replies, 1)
	assert.Len(t, replies[0].ParentDevice, 1)
	pd := replies[0].ParentDevice[0]
	assert.Equal(t, parentID, pd.ParentID)
	assert.Equal(t, operstate, pd.Operstate)
	assert.Equal(t, int(ffo), pd.FractionalFrequencyOffset)
	assert.Equal(t, int(ffoPPT), pd.FractionalFrequencyOffsetPPT)
}
