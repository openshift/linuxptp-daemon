package hardwareconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

// These tests verify that EEC/PPS parent devices are matched by their actual
// DPLL device type (as reported by a DPLL device dump) and never by parent ID
// parity or listing order.
//
// The fixtures mirror the GNR-D / Dell XR8720t (zl3073x) layout where the EEC
// device has an ODD id (11) and the PPS device has an EVEN id (12) - the
// opposite of what ID-parity and listing-order heuristics assume, and the
// exact arrangement that caused EEC inputs to stay "selectable" instead of
// being disconnected at DPLL initialization.

const (
	// testClockID matches the zl3073x DPLL of the failing system
	testClockID = uint64(0x6a5d225e28852a66)
	// EEC DPLL device id on the failing system (odd)
	testEECDeviceID = uint32(11)
	// PPS DPLL device id on the failing system (even)
	testPPSDeviceID = uint32(12)
	testIface       = "eno8703"
)

// gnrdDeviceReplies returns synthetic DPLL device replies as a real device
// dump would report them for the failing system: device 11 is EEC, device 12
// is PPS. Pin dumps do not carry the parent device type, so these replies are
// needed to populate the pin cache.
func gnrdDeviceReplies() []*dpll.DoDeviceGetReply {
	return []*dpll.DoDeviceGetReply{
		{ID: testEECDeviceID, Type: dpll.DpllTypeEEC},
		{ID: testPPSDeviceID, Type: dpll.DpllTypePPS},
	}
}

func inputParent(id uint32) dpll.PinParentDevice {
	return dpll.PinParentDevice{ParentID: id, Direction: dpll.PinDirectionInput}
}

func outputParent(id uint32) dpll.PinParentDevice {
	return dpll.PinParentDevice{ParentID: id, Direction: dpll.PinDirectionOutput}
}

func testPin(id uint32, boardLabel string, parents ...dpll.PinParentDevice) *dpll.PinInfo {
	return &dpll.PinInfo{
		ID:           id,
		ClockID:      testClockID,
		BoardLabel:   boardLabel,
		PackageLabel: boardLabel,
		ParentDevice: parents,
	}
}

// newManagerWithTypedPins builds a HardwareConfigManager whose pin cache
// (including parent device types from the given device dump) and clock ID
// cache are populated from programmatically constructed fixtures, so no test
// data dumps or command executors are needed.
func newManagerWithTypedPins(pins []*dpll.PinInfo, devices ...*dpll.DoDeviceGetReply) *HardwareConfigManager {
	hcm := newHardwareConfigManagerForTests()
	hcm.pinCache = buildPinCacheFromPins(pins, devices...)
	hcm.clockIDCache[testIface+":"] = testClockID
	return hcm
}

func testClockChain() *ptpv2alpha1.ClockChain {
	return &ptpv2alpha1.ClockChain{
		Structure: []ptpv2alpha1.Subsystem{
			{
				Name: "leader",
				DPLL: ptpv2alpha1.DPLL{NetworkInterface: testIface},
			},
		},
	}
}

// TestPinCacheParentDeviceType verifies that parent device types are resolved
// from a DPLL device dump, and that parents missing from the dump are reported
// as unknown instead of being guessed.
func TestPinCacheParentDeviceType(t *testing.T) {
	pins := []*dpll.PinInfo{
		testPin(23, "REF0N", inputParent(testEECDeviceID), inputParent(testPPSDeviceID)),
		// parent device 99 is deliberately absent from the device dump
		testPin(26, "REF2N", inputParent(99)),
	}
	cache := buildPinCacheFromPins(pins, gnrdDeviceReplies()...)

	parentType, known := cache.ParentDeviceType(testEECDeviceID)
	require.True(t, known, "EEC device must be known")
	assert.Equal(t, uint32(dpll.DpllTypeEEC), parentType, "device 11 must resolve to EEC")

	parentType, known = cache.ParentDeviceType(testPPSDeviceID)
	require.True(t, known, "PPS device must be known")
	assert.Equal(t, uint32(dpll.DpllTypePPS), parentType, "device 12 must resolve to PPS")

	_, known = cache.ParentDeviceType(99)
	assert.False(t, known, "device missing from the dump must be unknown")

	// A cache built without a device dump must not report any parent types
	untyped := buildPinCacheFromPins(pins)
	_, known = untyped.ParentDeviceType(testEECDeviceID)
	assert.False(t, known, "cache without a device dump must not report parent types")
}

// TestCreatePinCommandForDPLLDesiredStateByDeviceType verifies that EEC/PPS
// desired states are routed to the parent devices by device type. It uses the
// GNR-D device id arrangement (EEC odd, PPS even) and both parent listing
// orders, so neither ID parity nor listing order can produce a passing result.
func TestCreatePinCommandForDPLLDesiredStateByDeviceType(t *testing.T) {
	eecPriority := int64(1)
	tests := []struct {
		name    string
		parents []dpll.PinParentDevice
	}{
		{
			name: "EEC listed first (odd EEC id, even PPS id)",
			parents: []dpll.PinParentDevice{
				inputParent(testEECDeviceID),
				inputParent(testPPSDeviceID),
			},
		},
		{
			name: "PPS listed first",
			parents: []dpll.PinParentDevice{
				inputParent(testPPSDeviceID),
				inputParent(testEECDeviceID),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hcm := newManagerWithTypedPins([]*dpll.PinInfo{testPin(23, "REF0N", tt.parents...)}, gnrdDeviceReplies()...)

			desiredState := ptpv2alpha1.DPLLDesiredState{
				Subsystem:  "leader",
				BoardLabel: "REF0N",
				EEC: &ptpv2alpha1.PinState{
					State:    "disconnected",
					Priority: &eecPriority,
				},
				PPS: &ptpv2alpha1.PinState{
					State: "selectable",
				},
			}

			cmd, err := hcm.createPinCommandForDPLLDesiredState(desiredState, testClockChain())
			require.NoError(t, err)
			require.Len(t, cmd.PinParentCtl, 2, "both parents must be controlled")

			for _, pc := range cmd.PinParentCtl {
				switch pc.PinParentID {
				case testEECDeviceID:
					require.NotNil(t, pc.State, "EEC parent must have a state")
					assert.Equal(t, uint32(dpll.PinStateDisconnected), *pc.State,
						"EEC desired state must be applied to the EEC device")
					require.NotNil(t, pc.Prio, "EEC parent must have a priority")
					assert.Equal(t, uint32(eecPriority), *pc.Prio,
						"EEC priority must be applied to the EEC device")
				case testPPSDeviceID:
					require.NotNil(t, pc.State, "PPS parent must have a state")
					assert.Equal(t, uint32(dpll.PinStateSelectable), *pc.State,
						"PPS desired state must be applied to the PPS device")
					assert.Nil(t, pc.Prio, "PPS priority must not be set when not configured")
				default:
					t.Fatalf("unexpected parent device %d in command", pc.PinParentID)
				}
			}
		})
	}
}

// TestCreatePinCommandForDPLLDesiredStateUnknownType verifies that desired
// states are not applied to parents whose device type is unknown.
func TestCreatePinCommandForDPLLDesiredStateUnknownType(t *testing.T) {
	hcm := newManagerWithTypedPins([]*dpll.PinInfo{
		testPin(23, "REF0N", inputParent(99), inputParent(testPPSDeviceID)),
	}, gnrdDeviceReplies()...)

	desiredState := ptpv2alpha1.DPLLDesiredState{
		Subsystem:  "leader",
		BoardLabel: "REF0N",
		EEC:        &ptpv2alpha1.PinState{State: "disconnected"},
		PPS:        &ptpv2alpha1.PinState{State: "selectable"},
	}

	cmd, err := hcm.createPinCommandForDPLLDesiredState(desiredState, testClockChain())
	require.NoError(t, err)
	require.Len(t, cmd.PinParentCtl, 2)

	for _, pc := range cmd.PinParentCtl {
		if pc.PinParentID == 99 {
			assert.Nil(t, pc.State, "no state must be applied to a parent missing from the device dump")
			assert.Nil(t, pc.Prio, "no priority must be applied to a parent missing from the device dump")
		}
		if pc.PinParentID == testPPSDeviceID {
			require.NotNil(t, pc.State)
			assert.Equal(t, uint32(dpll.PinStateSelectable), *pc.State)
		}
	}
}

// TestBuildPinPriorityCommandByDeviceType verifies that EEC/PPS priorities
// from vendor defaults are routed to the parent devices by device type.
func TestBuildPinPriorityCommandByDeviceType(t *testing.T) {
	hcm := newManagerWithTypedPins([]*dpll.PinInfo{
		testPin(23, "REF0N", inputParent(testPPSDeviceID), inputParent(testEECDeviceID)),
	}, gnrdDeviceReplies()...)

	eecPrio := uint32(14)
	ppsPrio := uint32(6)
	cmd, ok := hcm.buildPinPriorityCommand(testClockID, "REF0N", &eecPrio, &ppsPrio)
	require.True(t, ok, "pin command must be built")
	require.Len(t, cmd.PinParentCtl, 2)

	for _, pc := range cmd.PinParentCtl {
		switch pc.PinParentID {
		case testEECDeviceID:
			require.NotNil(t, pc.Prio)
			assert.Equal(t, eecPrio, *pc.Prio, "EEC priority must be applied to the EEC device")
		case testPPSDeviceID:
			require.NotNil(t, pc.Prio)
			assert.Equal(t, ppsPrio, *pc.Prio, "PPS priority must be applied to the PPS device")
		default:
			t.Fatalf("unexpected parent device %d in command", pc.PinParentID)
		}
	}
}

// TestBuildPinCommandFromDefaultsStateOverridesByDeviceType verifies that
// EEC/PPS state overrides from vendor defaults are routed to output parents
// by device type, instead of overwriting every output parent.
func TestBuildPinCommandFromDefaultsStateOverridesByDeviceType(t *testing.T) {
	hcm := newManagerWithTypedPins([]*dpll.PinInfo{
		testPin(42, "OUT2N", outputParent(testPPSDeviceID), outputParent(testEECDeviceID)),
	}, gnrdDeviceReplies()...)

	subsystem := ptpv2alpha1.Subsystem{
		Name: "leader",
		DPLL: ptpv2alpha1.DPLL{NetworkInterface: testIface},
	}
	overrides := &PinDefault{
		EEC: &PinDefaultEntry{State: "connected"},
		PPS: &PinDefaultEntry{State: "disconnected"},
	}

	cmd, ok := hcm.buildPinCommandFromDefaults(subsystem, "", "OUT2N", overrides, nil, nil)
	require.True(t, ok, "pin command must be built")
	require.Len(t, cmd.PinParentCtl, 2)

	for _, pc := range cmd.PinParentCtl {
		switch pc.PinParentID {
		case testEECDeviceID:
			require.NotNil(t, pc.State, "EEC state override must be applied to the EEC device")
			assert.Equal(t, uint32(dpll.PinStateConnected), *pc.State)
		case testPPSDeviceID:
			require.NotNil(t, pc.State, "PPS state override must be applied to the PPS device")
			assert.Equal(t, uint32(dpll.PinStateDisconnected), *pc.State)
		default:
			t.Fatalf("unexpected parent device %d in command", pc.PinParentID)
		}
	}
}
