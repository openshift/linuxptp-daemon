package hardwareconfig

import (
	"fmt"
	"testing"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testModuleIce     = "ice"
	testModuleZl3073x = "zl3073x"
	testIfaceE825     = "eno5"
	testIfaceE810     = "ens4f0"
	testBusE825       = "0000:51:00.0"
	testBusE810       = "0000:13:00.0"
)

func TestGetClockIDFromPinParentChain(t *testing.T) {
	const (
		nicPinID         = uint32(5)
		zlParentPinID    = uint32(18)
		zlParentPinID2   = uint32(19)
		expectedClockID  = uint64(3549816820761526005)
		expectedClockID2 = uint64(9876543210123456789)
	)

	tests := []struct {
		name             string
		mockNetdevPin    func(string) (uint32, bool, error)
		mockPinQuerier   func(uint32) (*dpll.PinInfo, error)
		wantClockID      uint64
		wantErr          bool
		wantErrSubstring string
	}{
		{
			name: "happy path: NIC pin has zl3073x parent",
			mockNetdevPin: func(_ string) (uint32, bool, error) {
				return nicPinID, true, nil
			},
			mockPinQuerier: func(pinID uint32) (*dpll.PinInfo, error) {
				switch pinID {
				case nicPinID:
					return &dpll.PinInfo{
						ID:         nicPinID,
						ModuleName: testModuleIce,
						ClockID:    13036154006041183120,
						ParentPin: []dpll.PinParentPin{
							{ParentID: zlParentPinID, State: dpll.PinStateDisconnected},
							{ParentID: zlParentPinID2, State: dpll.PinStateDisconnected},
						},
					}, nil
				case zlParentPinID:
					return &dpll.PinInfo{
						ID:         zlParentPinID,
						ModuleName: testModuleZl3073x,
						ClockID:    expectedClockID,
					}, nil
				case zlParentPinID2:
					return &dpll.PinInfo{
						ID:         zlParentPinID2,
						ModuleName: testModuleZl3073x,
						ClockID:    expectedClockID,
					}, nil
				default:
					return nil, fmt.Errorf("unexpected pin ID %d", pinID)
				}
			},
			wantClockID: expectedClockID,
		},
		{
			name: "multiple parents: first non-ice parent wins",
			mockNetdevPin: func(_ string) (uint32, bool, error) {
				return nicPinID, true, nil
			},
			mockPinQuerier: func(pinID uint32) (*dpll.PinInfo, error) {
				switch pinID {
				case nicPinID:
					return &dpll.PinInfo{
						ID:         nicPinID,
						ModuleName: testModuleIce,
						ParentPin: []dpll.PinParentPin{
							{ParentID: zlParentPinID},
							{ParentID: zlParentPinID2},
						},
					}, nil
				case zlParentPinID:
					return &dpll.PinInfo{
						ID:         zlParentPinID,
						ModuleName: testModuleZl3073x,
						ClockID:    expectedClockID,
					}, nil
				case zlParentPinID2:
					return &dpll.PinInfo{
						ID:         zlParentPinID2,
						ModuleName: "other_dpll",
						ClockID:    expectedClockID2,
					}, nil
				default:
					return nil, fmt.Errorf("unexpected pin ID %d", pinID)
				}
			},
			wantClockID: expectedClockID,
		},
		{
			name: "non-zl3073x parent module still returns clock ID",
			mockNetdevPin: func(_ string) (uint32, bool, error) {
				return nicPinID, true, nil
			},
			mockPinQuerier: func(pinID uint32) (*dpll.PinInfo, error) {
				switch pinID {
				case nicPinID:
					return &dpll.PinInfo{
						ID:         nicPinID,
						ModuleName: testModuleIce,
						ParentPin: []dpll.PinParentPin{
							{ParentID: zlParentPinID},
						},
					}, nil
				case zlParentPinID:
					return &dpll.PinInfo{
						ID:         zlParentPinID,
						ModuleName: "other_dpll",
						ClockID:    expectedClockID2,
					}, nil
				default:
					return nil, fmt.Errorf("unexpected pin ID %d", pinID)
				}
			},
			wantClockID: expectedClockID2,
		},
		{
			name: "no IFLA_DPLL_PIN on NIC",
			mockNetdevPin: func(_ string) (uint32, bool, error) {
				return 0, false, nil
			},
			wantErr:          true,
			wantErrSubstring: "no DPLL pin found",
		},
		{
			name: "netdev pin getter error",
			mockNetdevPin: func(_ string) (uint32, bool, error) {
				return 0, false, fmt.Errorf("netlink route failed")
			},
			wantErr:          true,
			wantErrSubstring: "failed to get DPLL pin",
		},
		{
			name: "DPLL pin query fails",
			mockNetdevPin: func(_ string) (uint32, bool, error) {
				return nicPinID, true, nil
			},
			mockPinQuerier: func(_ uint32) (*dpll.PinInfo, error) {
				return nil, fmt.Errorf("DPLL netlink connection failed")
			},
			wantErr:          true,
			wantErrSubstring: "failed to query DPLL pin",
		},
		{
			name: "pin has no parent pins",
			mockNetdevPin: func(_ string) (uint32, bool, error) {
				return nicPinID, true, nil
			},
			mockPinQuerier: func(_ uint32) (*dpll.PinInfo, error) {
				return &dpll.PinInfo{
					ID:         nicPinID,
					ModuleName: testModuleIce,
					ParentPin:  nil,
				}, nil
			},
			wantErr:          true,
			wantErrSubstring: "has no parent pins",
		},
		{
			name: "all parents share same module as NIC",
			mockNetdevPin: func(_ string) (uint32, bool, error) {
				return nicPinID, true, nil
			},
			mockPinQuerier: func(pinID uint32) (*dpll.PinInfo, error) {
				return &dpll.PinInfo{
					ID:         pinID,
					ModuleName: testModuleIce,
					ParentPin: []dpll.PinParentPin{
						{ParentID: zlParentPinID},
					},
				}, nil
			},
			wantErr:          true,
			wantErrSubstring: "no external DPLL parent found",
		},
		{
			name: "parent pin query fails but next parent succeeds",
			mockNetdevPin: func(_ string) (uint32, bool, error) {
				return nicPinID, true, nil
			},
			mockPinQuerier: func(pinID uint32) (*dpll.PinInfo, error) {
				switch pinID {
				case nicPinID:
					return &dpll.PinInfo{
						ID:         nicPinID,
						ModuleName: testModuleIce,
						ParentPin: []dpll.PinParentPin{
							{ParentID: zlParentPinID},
							{ParentID: zlParentPinID2},
						},
					}, nil
				case zlParentPinID:
					return nil, fmt.Errorf("transient error")
				case zlParentPinID2:
					return &dpll.PinInfo{
						ID:         zlParentPinID2,
						ModuleName: testModuleZl3073x,
						ClockID:    expectedClockID,
					}, nil
				default:
					return nil, fmt.Errorf("unexpected pin ID %d", pinID)
				}
			},
			wantClockID: expectedClockID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origNetdev := netdevDpllPinGetter
			origQuerier := dpllPinQuerier
			defer func() {
				netdevDpllPinGetter = origNetdev
				dpllPinQuerier = origQuerier
			}()

			netdevDpllPinGetter = tt.mockNetdevPin
			if tt.mockPinQuerier != nil {
				dpllPinQuerier = tt.mockPinQuerier
			}

			clockID, err := getClockIDFromPinParentChain("eno8303")
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrSubstring != "" {
					assert.Contains(t, err.Error(), tt.wantErrSubstring)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantClockID, clockID)
		})
	}
}

func TestGetClockIDFromInterfaceWithCache_E825Derivation(t *testing.T) {
	const expectedClockID = uint64(3549816820761526005)

	origNetdev := netdevDpllPinGetter
	origQuerier := dpllPinQuerier
	origCmd := commandExecutor
	defer func() {
		netdevDpllPinGetter = origNetdev
		dpllPinQuerier = origQuerier
		commandExecutor = origCmd
	}()

	setupE825PinMocks := func() {
		netdevDpllPinGetter = func(_ string) (uint32, bool, error) {
			return 5, true, nil
		}
		dpllPinQuerier = func(pinID uint32) (*dpll.PinInfo, error) {
			if pinID == 5 {
				return &dpll.PinInfo{
					ID: 5, ModuleName: testModuleIce,
					ParentPin: []dpll.PinParentPin{{ParentID: 18}},
				}, nil
			}
			return &dpll.PinInfo{
				ID: 18, ModuleName: testModuleZl3073x, ClockID: expectedClockID,
			}, nil
		}
	}

	t.Run("legacy detection: E825 via lspci uses pin chain when no hwDefPath", func(t *testing.T) {
		mockCmd := NewMockCommandExecutor()
		mockCmd.SetResponse("ethtool", []string{"-i", testIfaceE825}, "driver: ice\nbus-info: "+testBusE825)
		mockCmd.SetResponse("lspci", []string{"-s", testBusE825}, testBusE825+" Ethernet controller: Intel Corporation Ethernet Controller E825-C")
		commandExecutor = mockCmd
		setupE825PinMocks()

		clockID, err := GetClockIDFromInterfaceWithCache(testIfaceE825, "", nil)
		require.NoError(t, err)
		assert.Equal(t, expectedClockID, clockID)
	})

	t.Run("config-driven: hwDefPath with devlinkPinChain method", func(t *testing.T) {
		mockCmd := NewMockCommandExecutor()
		mockCmd.SetResponse("ethtool", []string{"-i", testIfaceE825}, "driver: ice\nbus-info: "+testBusE825)
		commandExecutor = mockCmd
		setupE825PinMocks()

		clockID, err := GetClockIDFromInterfaceWithCache(testIfaceE825, HwDefIntelE825, nil)
		require.NoError(t, err)
		assert.Equal(t, expectedClockID, clockID)
	})

	t.Run("fallback to module-name scan on derivation failure", func(t *testing.T) {
		mockCmd := NewMockCommandExecutor()
		mockCmd.SetResponse("ethtool", []string{"-i", testIfaceE825}, "driver: ice\nbus-info: "+testBusE825)
		commandExecutor = mockCmd

		netdevDpllPinGetter = func(_ string) (uint32, bool, error) {
			return 0, false, fmt.Errorf("IFLA_DPLL_PIN not supported")
		}

		mockPins := []*dpll.PinInfo{
			{ID: 10, ModuleName: testModuleZl3073x, ClockID: expectedClockID, BoardLabel: "REF1P"},
		}
		SetupMockDpllPinsForTestsWithData(mockPins)
		defer TeardownMockDpllPinsForTests()

		clockID, err := GetClockIDFromInterfaceWithCache(testIfaceE825, HwDefIntelE825, nil)
		require.NoError(t, err)
		assert.Equal(t, expectedClockID, clockID)
	})

	t.Run("config-driven: direct method uses serial number", func(t *testing.T) {
		mockCmd := NewMockCommandExecutor()
		mockCmd.SetResponse("ethtool", []string{"-i", testIfaceE810}, "driver: ice\nbus-info: "+testBusE810)
		mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/" + testBusE810}, //nolint:goconst
			"pci/"+testBusE810+":\n  serial_number 50-7c-6f-ff-ff-1f-b5-80")
		commandExecutor = mockCmd

		clockID, err := GetClockIDFromInterfaceWithCache(testIfaceE810, HwDefIntelE810, nil)
		require.NoError(t, err)
		assert.Equal(t, uint64(0x507c6fffff1fb580), clockID)
	})
}

func TestGetClockIDResolutionMethod(t *testing.T) {
	origCmd := commandExecutor
	defer func() { commandExecutor = origCmd }()

	t.Run("hwDefPath with devlinkPinChain returns devlinkPinChain", func(t *testing.T) {
		method := getClockIDResolutionMethod(HwDefIntelE825, testBusE825)
		assert.Equal(t, ClockIDMethodDevlinkPinChain, method)
	})

	t.Run("hwDefPath with direct returns direct", func(t *testing.T) {
		method := getClockIDResolutionMethod(HwDefIntelE810, testBusE810)
		assert.Equal(t, ClockIDMethodDirect, method)
	})

	t.Run("no hwDefPath with E825 lspci falls back to devlinkPinChain", func(t *testing.T) {
		mockCmd := NewMockCommandExecutor()
		mockCmd.SetResponse("lspci", []string{"-s", testBusE825}, "E825-C controller")
		commandExecutor = mockCmd

		method := getClockIDResolutionMethod("", testBusE825)
		assert.Equal(t, ClockIDMethodDevlinkPinChain, method)
	})

	t.Run("no hwDefPath with non-E825 lspci falls back to direct", func(t *testing.T) {
		mockCmd := NewMockCommandExecutor()
		mockCmd.SetResponse("lspci", []string{"-s", testBusE810}, "E810-XXVDA4T controller")
		commandExecutor = mockCmd

		method := getClockIDResolutionMethod("", testBusE810)
		assert.Equal(t, ClockIDMethodDirect, method)
	})
}
