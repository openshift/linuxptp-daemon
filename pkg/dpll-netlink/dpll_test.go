package dpll_netlink

import (
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
