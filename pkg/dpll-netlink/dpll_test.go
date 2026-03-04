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
