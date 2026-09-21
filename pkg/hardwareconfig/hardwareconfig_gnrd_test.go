package hardwareconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestGNRDHardwareConfigFullFlow tests the complete flow for GNRD hardware config:
// - Loading hardware config YAML
// - Loading pin cache from testdata
// - Processing defaults from intel/e825
// - Processing init transition
// - Verifying DPLL commands are correct
func TestGNRDHardwareConfigFullFlow(t *testing.T) {
	// Setup test environment and get the actual clock ID from pin cache
	actualClockID := setupGNRDTestEnvironment(t)
	defer teardownGNRDTestEnvironment()

	// Load GNRD hardware config
	hwConfig, err := loadHardwareConfigFromFile("testdata/gnrd-hwconfig.yaml")
	if !assert.NoError(t, err, "Failed to load gnrd-hwconfig.yaml") {
		return
	}
	if !assert.NotNil(t, hwConfig, "Hardware config should not be nil") {
		return
	}

	t.Logf("✓ Loaded hardware config: %s", hwConfig.Name)
	t.Logf("  Profile: %s", *hwConfig.Spec.Profile.Name)
	t.Logf("  Related PTP Profile: %s", hwConfig.Spec.RelatedPtpProfileName)

	// Create hardware config manager
	hcm := newHardwareConfigManagerForTests()
	defer hcm.resetExecutors()

	// Track captured DPLL commands
	var capturedDpllCommands []dpll.PinParentDeviceCtl
	var capturedSysFSCommands []SysFSCommand

	// Override executors to capture commands instead of sending to hardware
	dpllExecutor := func(cmds []dpll.PinParentDeviceCtl) error {
		snapshot := make([]dpll.PinParentDeviceCtl, len(cmds))
		copy(snapshot, cmds)
		capturedDpllCommands = append(capturedDpllCommands, snapshot...)
		t.Logf("  Captured %d DPLL commands", len(cmds))
		for i, cmd := range cmds {
			t.Logf("    [%d] Pin ID=%d, Freq=%v, ESync=%v, ParentCtls=%d",
				i+1, cmd.ID, ptrValueOrNil(cmd.Frequency), ptrValueOrNil(cmd.EsyncFrequency), len(cmd.PinParentCtl))
		}
		return nil
	}

	sysFSExecutor := func(path, value string) error {
		capturedSysFSCommands = append(capturedSysFSCommands, SysFSCommand{Path: path, Value: value})
		t.Logf("  Captured SysFS: %s = %s", path, value)
		return nil
	}

	hcm.overrideExecutors(dpllExecutor, sysFSExecutor)

	// Update hardware config (this processes defaults and structure)
	t.Logf("\n=== Phase 1: Processing Hardware Config (Defaults + Structure) ===")
	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{*hwConfig})
	if !assert.NoError(t, err, "Failed to update hardware config") {
		return
	}

	structureCommandCount := len(capturedDpllCommands)
	t.Logf("\n✓ Structure processing complete: %d DPLL commands captured", structureCommandCount)

	// Apply hardware configs for profile (this applies defaults and init)
	t.Logf("\n=== Phase 2: Applying Hardware Config for Profile ===")
	profile := &ptpv1.PtpProfile{
		Name: stringPtr("t-bc_01-tbc-tr"),
	}

	capturedDpllCommands = nil // Reset to capture only init commands
	capturedSysFSCommands = nil

	err = hcm.ApplyHardwareConfigsForProfile(profile)
	if !assert.NoError(t, err, "Failed to apply hardware config for profile") {
		return
	}

	initCommandCount := len(capturedDpllCommands)
	initSysFSCount := len(capturedSysFSCommands)
	t.Logf("\n✓ Init processing complete: %d DPLL commands, %d SysFS commands", initCommandCount, initSysFSCount)

	// Validate structure commands
	t.Run("validate_structure_commands", func(t *testing.T) {
		validateGNRDStructureCommands(t, hcm, actualClockID)
	})

	// Validate init commands
	t.Run("validate_init_commands", func(t *testing.T) {
		validateGNRDInitCommands(t, capturedDpllCommands, capturedSysFSCommands, actualClockID)
	})

	// Validate vendor defaults are applied
	t.Run("validate_vendor_defaults", func(t *testing.T) {
		validateGNRDVendorDefaults(t)
	})

	// Test behavior transitions
	t.Run("validate_behavior_transitions", func(t *testing.T) {
		validateGNRDBehaviorTransitions(t, hcm, hwConfig, profile)
	})
}

// setupGNRDTestEnvironment sets up the test environment for GNRD tests
func setupGNRDTestEnvironment(t *testing.T) uint64 {
	// Setup mock PTP device resolver with only 1 device per interface
	mockDevices := map[string][]string{
		"/sys/class/net/eno5/device/ptp/ptp*/pins/SDP0": {
			"/sys/class/net/eno5/device/ptp/ptp0/pins/SDP0",
		},
		"/sys/class/net/eno5/device/ptp/ptp*/period": {
			"/sys/class/net/eno5/device/ptp/ptp0/period",
		},
	}
	SetupMockPtpDeviceResolverWithDevices(mockDevices)

	// Load pins from perla2-pins.json
	mockGetter, err := CreateMockDpllPinsGetterFromFile("../daemon/testdata/perla2-pins.json")
	if !assert.NoError(t, err, "Failed to create mock getter from perla2-pins.json") {
		t.FailNow()
	}
	SetDpllPinsGetter(mockGetter)

	// Verify pins loaded correctly
	cache, err := GetDpllPins()
	if !assert.NoError(t, err, "Failed to get DPLL pins") {
		t.FailNow()
	}
	t.Logf("✓ Loaded %d pins from perla2-pins.json", cache.Count())

	// Get the actual clock ID from the loaded pins
	var actualClockID uint64
	for clockID := range cache.BoardLabelPins {
		actualClockID = clockID
		t.Logf("✓ Using clock ID from pin cache: %#x", actualClockID)
		break
	}

	// Setup mock command executor for clock ID resolution
	// For PERLA hardware, we use the E825 + zl3073x DPLL workaround
	mockCmd := NewMockCommandExecutor()

	// Mock ethtool to return bus address
	mockCmd.SetResponse("ethtool", []string{"-i", "eno5"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("ethtool", []string{"-i", "eno2"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("ethtool", []string{"-i", "eno3"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("ethtool", []string{"-i", "eno4"}, "driver: ice\nbus-info: 0000:51:00.0")

	// Mock lspci to return E825 device (triggers PERLA workaround)
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E825-C for backplane")

	// These serial number responses won't be used due to PERLA workaround, but keep them as fallback
	serialNumber := fmt.Sprintf("%02x-%02x-%02x-%02x-%02x-%02x-%02x-%02x",
		(actualClockID>>56)&0xff, (actualClockID>>48)&0xff, (actualClockID>>40)&0xff, (actualClockID>>32)&0xff,
		(actualClockID>>24)&0xff, (actualClockID>>16)&0xff, (actualClockID>>8)&0xff, actualClockID&0xff)
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number "+serialNumber)

	SetCommandExecutor(mockCmd)

	return actualClockID
}

// teardownGNRDTestEnvironment tears down the test environment
func teardownGNRDTestEnvironment() {
	TeardownMockPtpDeviceResolver()
	TeardownMockDpllPinsForTests()
	ResetCommandExecutor()
}

// validateGNRDStructureCommands validates the structure commands (defaults)
func validateGNRDStructureCommands(t *testing.T, hcm *HardwareConfigManager, clockID uint64) {
	t.Logf("\n=== Validating Structure Commands (Vendor Defaults) ===")

	// Get the enriched config to access structure commands
	hcm.mu.RLock()
	defer hcm.mu.RUnlock()

	if !assert.Len(t, hcm.hardwareConfigs, 1, "Should have 1 hardware config") {
		return
	}
	enriched := hcm.hardwareConfigs[0]

	structureCommands := enriched.structurePinCommands
	t.Logf("Structure commands: %d", len(structureCommands))

	// Load e825 defaults for validation
	hwSpec, err := LoadHardwareDefaults(HwDefIntelE825, nil)
	if !assert.NoError(t, err, "Failed to load e825 defaults") {
		return
	}
	if !assert.NotNil(t, hwSpec, "Hardware spec should not be nil") {
		return
	}

	t.Logf("✓ Loaded intel/e825 defaults: %d pin defaults defined", len(hwSpec.PinDefaults))

	// Use the clock ID from pin cache
	t.Logf("Using clock ID: %#x", clockID)

	cache, _ := GetDpllPins()
	expectedPins := []string{
		"GNR-D_SDP0",
		"GNR-D_SDP1",
		"GNSS_1PPS_IN",
		"SMA1_IN",
	}

	for _, pinLabel := range expectedPins {
		pin, found := cache.GetPin(clockID, pinLabel)
		if found {
			t.Logf("  ✓ Pin %s (ID=%d) exists in cache", pinLabel, pin.ID)

			// Check if this pin has a default configuration in structure commands
			hasDefault := false
			for _, cmd := range structureCommands {
				if cmd.ID == pin.ID {
					hasDefault = true
					t.Logf("    → Found default command for pin %s", pinLabel)
					break
				}
			}
			if hasDefault {
				t.Logf("    ✓ Default configuration applied")
			}
		} else {
			t.Logf("  ⚠ Pin %s not found in cache", pinLabel)
		}
	}
}

// validateGNRDInitCommands validates the init transition commands
func validateGNRDInitCommands(t *testing.T, dpllCommands []dpll.PinParentDeviceCtl, sysFSCommands []SysFSCommand, clockID uint64) {
	t.Logf("\n=== Validating Init Commands ===")

	// According to gnrd-hwconfig.yaml, the init condition ("Initialize T-BC") should:
	// 1. Configure SysFS for SDP2: /sys/class/net/{interface}/device/ptp/ptp*/pins/SDP2 = "0 0"
	// 2. Configure SysFS for SDP0: /sys/class/net/{interface}/device/ptp/ptp*/pins/SDP0 = "0 0"
	// 3. Set GNSS_1PPS_IN to disconnected (both EEC and PPS)
	// Note: The period sysfs command and SDP0="2 1" are in the "PTP Source Locked" condition, not init

	t.Logf("Init DPLL commands: %d", len(dpllCommands))
	t.Logf("Init SysFS commands: %d", len(sysFSCommands))

	// Validate SysFS commands
	foundSDP0 := false
	foundSDP2 := false

	for _, cmd := range sysFSCommands {
		if strings.Contains(cmd.Path, "pins/SDP0") {
			foundSDP0 = true
			assert.Equal(t, "0 0", cmd.Value, "SDP0 value should be '0 0' in init")
			t.Logf("  ✓ SDP0 configured: %s = %s", cmd.Path, cmd.Value)
		}
		if strings.Contains(cmd.Path, "pins/SDP2") {
			foundSDP2 = true
			assert.Equal(t, "0 0", cmd.Value, "SDP2 value should be '0 0' in init")
			t.Logf("  ✓ SDP2 configured: %s = %s", cmd.Path, cmd.Value)
		}
	}

	// With 1 PTP device per interface, we should have exactly 1 of each sysfs command
	assert.True(t, foundSDP0, "Should have SDP0 sysfs command in init")
	assert.True(t, foundSDP2, "Should have SDP2 sysfs command in init")

	// Validate DPLL commands for key pins
	cache, _ := GetDpllPins()
	t.Logf("Using clock ID: %#x", clockID)

	// Check GNSS_1PPS_IN should be disconnected (both EEC and PPS)
	gnssPin, gnssFound := cache.GetPin(clockID, "GNSS_1PPS_IN")
	if gnssFound {
		for _, cmd := range dpllCommands {
			if cmd.ID == gnssPin.ID && len(cmd.PinParentCtl) > 0 {
				for _, pc := range cmd.PinParentCtl {
					if pc.State != nil {
						assert.Equal(t, uint32(dpll.PinStateDisconnected), *pc.State,
							"GNSS_1PPS_IN should be disconnected in init")
						t.Logf("  ✓ GNSS_1PPS_IN (ID=%d) set to disconnected", gnssPin.ID)
					}
				}
			}
		}
	}

	// Check GNR-D_SDP0: EEC should be disconnected, PPS should be selectable
	sdp0Pin, sdp0Found := cache.GetPin(clockID, "GNR-D_SDP0")
	if sdp0Found {
		for _, cmd := range dpllCommands {
			if cmd.ID == sdp0Pin.ID && len(cmd.PinParentCtl) > 0 {
				for _, pc := range cmd.PinParentCtl {
					if pc.State != nil {
						// ParentID 0 = EEC (should be disconnected)
						// ParentID 1 = PPS (should be selectable)
						switch pc.PinParentID {
						case 0:
							assert.Equal(t, uint32(dpll.PinStateDisconnected), *pc.State,
								"GNR-D_SDP0 EEC should be disconnected in init")
							t.Logf("  ✓ GNR-D_SDP0 (ID=%d) EEC set to disconnected", sdp0Pin.ID)
						case 1:
							assert.Equal(t, uint32(dpll.PinStateSelectable), *pc.State,
								"GNR-D_SDP0 PPS should be selectable in init")
							t.Logf("  ✓ GNR-D_SDP0 (ID=%d) PPS set to selectable", sdp0Pin.ID)
						}
					}
				}
			}
		}
	}
}

// validateGNRDVendorDefaults validates that e825 vendor defaults are properly loaded
func validateGNRDVendorDefaults(t *testing.T) {
	t.Logf("\n=== Validating Vendor Defaults (intel/e825) ===")

	hwSpec, err := LoadHardwareDefaults(HwDefIntelE825, nil)
	if !assert.NoError(t, err, "Failed to load e825 defaults") {
		return
	}
	if !assert.NotNil(t, hwSpec, "Hardware spec should not be nil") {
		return
	}

	// Validate key fields from e825/defaults.yaml
	assert.NotNil(t, hwSpec.ClockIDTransformation, "Clock ID transformation should be defined")
	assert.Equal(t, "devlinkPinChain", hwSpec.ClockIDTransformation.Method, "E825 should use devlinkPinChain to resolve clock ID via NIC pin parent chain")
	t.Logf("  ✓ Clock ID transformation method: %s", hwSpec.ClockIDTransformation.Method)

	assert.NotEmpty(t, hwSpec.PinDefaults, "Pin defaults should not be empty")
	t.Logf("  ✓ Pin defaults defined: %d pins", len(hwSpec.PinDefaults))

	// Validate specific pin defaults (only pins that are actually defined in defaults.yaml)
	expectedDefaults := map[string]struct {
		eecPrio  *int64
		ppsPrio  *int64
		eecState string
		ppsState string
	}{
		"GNR-D_SDP0": {eecState: "disconnected", ppsState: "selectable"},
		"GNR-D_SDP2": {eecState: "disconnected", ppsState: "selectable"},
	}

	for pinLabel, expected := range expectedDefaults {
		pinDef, found := hwSpec.PinDefaults[pinLabel]
		if assert.True(t, found, "Pin %s should have defaults", pinLabel) {
			t.Logf("  Pin: %s", pinLabel)
			if expected.eecPrio != nil && pinDef.EEC != nil && pinDef.EEC.Priority != nil {
				assert.Equal(t, *expected.eecPrio, *pinDef.EEC.Priority,
					"Pin %s EEC priority mismatch", pinLabel)
				t.Logf("    ✓ EEC priority: %d", *pinDef.EEC.Priority)
			}
			if expected.ppsPrio != nil && pinDef.PPS != nil && pinDef.PPS.Priority != nil {
				assert.Equal(t, *expected.ppsPrio, *pinDef.PPS.Priority,
					"Pin %s PPS priority mismatch", pinLabel)
				t.Logf("    ✓ PPS priority: %d", *pinDef.PPS.Priority)
			}
			if expected.eecState != "" && pinDef.EEC != nil {
				assert.Equal(t, expected.eecState, pinDef.EEC.State,
					"Pin %s EEC state mismatch", pinLabel)
				t.Logf("    ✓ EEC state: %s", pinDef.EEC.State)
			}
			if expected.ppsState != "" && pinDef.PPS != nil {
				assert.Equal(t, expected.ppsState, pinDef.PPS.State,
					"Pin %s PPS state mismatch", pinLabel)
				t.Logf("    ✓ PPS state: %s", pinDef.PPS.State)
			}
		}
	}

	// Validate eSync command sequences (optional - may not be defined for all hardware)
	if hwSpec.PinEsyncCommands != nil {
		if len(hwSpec.PinEsyncCommands.Outputs) > 0 {
			t.Logf("  ✓ eSync output command sequences: %d steps", len(hwSpec.PinEsyncCommands.Outputs))
		}
		if len(hwSpec.PinEsyncCommands.Inputs) > 0 {
			t.Logf("  ✓ eSync input command sequences: %d steps", len(hwSpec.PinEsyncCommands.Inputs))
		}
		if len(hwSpec.PinEsyncCommands.Outputs) == 0 && len(hwSpec.PinEsyncCommands.Inputs) == 0 {
			t.Logf("  ⚠ eSync commands defined but empty")
		}
	} else {
		t.Logf("  ⚠ eSync commands not defined (optional)")
	}
}

// validateGNRDBehaviorTransitions validates all behavior transitions
func validateGNRDBehaviorTransitions(t *testing.T, hcm *HardwareConfigManager, hwConfig *ptpv2alpha1.HardwareConfig, profile *ptpv1.PtpProfile) {
	t.Logf("\n=== Validating Behavior Transitions ===")

	clockChain := hwConfig.Spec.Profile.ClockChain
	if !assert.NotNil(t, clockChain, "Clock chain should not be nil") {
		return
	}
	if !assert.NotNil(t, clockChain.Behavior, "Behavior should not be nil") {
		return
	}

	conditions := clockChain.Behavior.Conditions
	t.Logf("Total conditions: %d", len(conditions))

	// Test each condition
	for i, condition := range conditions {
		t.Run(fmt.Sprintf("condition_%d_%s", i+1, condition.Name), func(t *testing.T) {
			t.Logf("\n--- Testing Condition: %s ---", condition.Name)
			t.Logf("  Triggers: %d", len(condition.Triggers))
			t.Logf("  Desired states: %d", len(condition.DesiredStates))

			// Capture commands for this condition
			var conditionDpllCommands []dpll.PinParentDeviceCtl
			var conditionSysFSCommands []SysFSCommand

			dpllExecutor := func(cmds []dpll.PinParentDeviceCtl) error {
				snapshot := make([]dpll.PinParentDeviceCtl, len(cmds))
				copy(snapshot, cmds)
				conditionDpllCommands = append(conditionDpllCommands, snapshot...)
				return nil
			}

			sysFSExecutor := func(path, value string) error {
				conditionSysFSCommands = append(conditionSysFSCommands, SysFSCommand{Path: path, Value: value})
				return nil
			}

			hcm.overrideExecutors(dpllExecutor, sysFSExecutor)

			// Apply condition
			err := hcm.applyDesiredStatesInOrder(condition, *profile.Name, hwConfig.Spec.Profile.ClockChain)
			if !assert.NoError(t, err, "Failed to apply condition %s", condition.Name) {
				return
			}

			t.Logf("  ✓ Applied successfully: %d DPLL commands, %d SysFS commands",
				len(conditionDpllCommands), len(conditionSysFSCommands))

			// Log details about the commands
			for j, cmd := range conditionDpllCommands {
				cache, _ := GetDpllPins()
				pinLabel := ""
				for clkID, pins := range cache.BoardLabelPins {
					for label, pin := range pins {
						if pin.ID == cmd.ID {
							pinLabel = label
							t.Logf("    DPLL[%d]: Pin=%s (ID=%d, ClockID=%#x)", j+1, pinLabel, cmd.ID, clkID)
							break
						}
					}
				}
				if len(cmd.PinParentCtl) > 0 {
					for k, pc := range cmd.PinParentCtl {
						stateStr := ""
						if pc.State != nil {
							switch *pc.State {
							case dpll.PinStateConnected:
								stateStr = "connected"
							case dpll.PinStateDisconnected:
								stateStr = "disconnected"
							case dpll.PinStateSelectable:
								stateStr = "selectable"
							}
						}
						prioStr := ""
						if pc.Prio != nil {
							prioStr = fmt.Sprintf("%d", *pc.Prio)
						}
						t.Logf("      ParentCtl[%d]: ParentID=%d, State=%s, Prio=%s",
							k+1, pc.PinParentID, stateStr, prioStr)
					}
				}
			}

			for j, cmd := range conditionSysFSCommands {
				t.Logf("    SysFS[%d]: %s = %s", j+1, cmd.Path, cmd.Value)
			}
		})
	}
}

// Helper functions

func ptrValueOrNil(ptr *uint64) string {
	if ptr == nil {
		return ""
	}
	return fmt.Sprintf("%d", *ptr)
}

// TestPerla2PinsLoading tests that perla2-pins.json loads correctly
func TestPerla2PinsLoading(t *testing.T) {
	// Load pins from file
	data, err := os.ReadFile("../daemon/testdata/perla2-pins.json")
	if !assert.NoError(t, err, "Failed to read perla2-pins.json") {
		return
	}

	var hrPins []*PinInfoHR
	err = json.Unmarshal(data, &hrPins)
	if !assert.NoError(t, err, "Failed to parse perla2-pins.json") {
		return
	}

	t.Logf("Loaded %d pins from perla2-pins.json", len(hrPins))

	// Verify clock ID is consistent
	var clockIDStr string
	for i, pin := range hrPins {
		if i == 0 {
			clockIDStr = pin.ClockID
			// Parse clock ID from hex string
			if parsedID, parseErr := strconv.ParseUint(strings.TrimPrefix(clockIDStr, "0x"), 16, 64); parseErr == nil {
				t.Logf("Clock ID: %#x (from %s)", parsedID, clockIDStr)
			} else {
				t.Logf("Clock ID: %s", clockIDStr)
			}
		} else {
			assert.Equal(t, clockIDStr, pin.ClockID, "All pins should have same clock ID")
		}
	}

	// Verify key pins exist
	expectedPins := []string{
		"GNR-D_SDP0",
		"GNR_D_SDP1", // Note: uses underscore, not dash
		"GNR-D_SDP2",
		"GNSS_1PPS_IN",
		"SMA1_IN",
		"SMA2_OUT",
	}

	foundPins := make(map[string]bool)
	for _, pin := range hrPins {
		if pin.BoardLabel != "" {
			foundPins[pin.BoardLabel] = true
		}
	}

	for _, expected := range expectedPins {
		assert.True(t, foundPins[expected], "Expected pin %s not found", expected)
		if foundPins[expected] {
			t.Logf("  ✓ Found pin: %s", expected)
		}
	}
}

// TestPerla4PinsLoading tests that pins-perla4.json (Dell XR8720t / zl3073x) loads correctly
func TestPerla4PinsLoading(t *testing.T) {
	// Load pins from file
	data, err := os.ReadFile("testdata/pins-perla4.json")
	if !assert.NoError(t, err, "Failed to read pins-perla4.json") {
		return
	}

	var hrPins []*PinInfoHR
	err = json.Unmarshal(data, &hrPins)
	if !assert.NoError(t, err, "Failed to parse pins-perla4.json") {
		return
	}

	t.Logf("Loaded %d pins from pins-perla4.json", len(hrPins))

	// Verify clock ID is consistent
	var clockIDStr string
	for i, pin := range hrPins {
		if i == 0 {
			clockIDStr = pin.ClockID
			if parsedID, parseErr := strconv.ParseUint(strings.TrimPrefix(clockIDStr, "0x"), 16, 64); parseErr == nil {
				t.Logf("Clock ID: %#x (from %s)", parsedID, clockIDStr)
			} else {
				t.Logf("Clock ID: %s", clockIDStr)
			}
		} else {
			assert.Equal(t, clockIDStr, pin.ClockID, "All pins should have same clock ID")
		}
	}

	// Verify module name is zl3073x (Dell/PERLA4 hardware uses zl3073x DPLL)
	for _, pin := range hrPins {
		assert.Equal(t, "zl3073x", pin.ModuleName, "All pins should be from zl3073x module")
	}

	// Verify key pins exist
	expectedPins := []string{
		"ETH01_SDP_TIMESYNC_0",
		"ETH01_SDP_TIMESYNC_2",
		"GNSS_1PPS_IN",
		"GNSS_10M_IN",
		"1EPPS_IN",
		"ETH01_SDP_TIMESYNC_1",
		"ETH01_SDP_TIMESYNC_3",
		"EPPS_10M_ADD_IN_CARD_SYNC",
	}

	foundPins := make(map[string]bool)
	for _, pin := range hrPins {
		if pin.BoardLabel != "" {
			foundPins[pin.BoardLabel] = true
		}
	}

	for _, expected := range expectedPins {
		assert.True(t, foundPins[expected], "Expected pin %s not found", expected)
		if foundPins[expected] {
			t.Logf("  ✓ Found pin: %s", expected)
		}
	}
}

// TestDellXR8720tBehaviorTransitions validates that dell/XR8720t behavior profiles
// can be loaded and processed with perla4 pin data
func TestDellXR8720tBehaviorTransitions(t *testing.T) {
	// Setup test environment using perla4 pins
	mockGetter, err := CreateMockDpllPinsGetterFromFile("testdata/pins-perla4.json")
	if !assert.NoError(t, err, "Failed to create mock getter from pins-perla4.json") {
		t.FailNow()
	}
	SetDpllPinsGetter(mockGetter)
	defer TeardownMockDpllPinsForTests()

	// Verify pins loaded correctly
	cache, err := GetDpllPins()
	if !assert.NoError(t, err, "Failed to get DPLL pins") {
		t.FailNow()
	}
	t.Logf("✓ Loaded %d pins from pins-perla4.json", cache.Count())

	// Get the clock ID from the loaded pins
	var actualClockID uint64
	for clockID := range cache.BoardLabelPins {
		actualClockID = clockID
		t.Logf("✓ Using clock ID from pin cache: %#x", actualClockID)
		break
	}

	// Verify key pins referenced in dell/XR8720t behavior-profiles.yaml exist
	ptpInputPin, found := cache.GetPin(actualClockID, "ETH01_SDP_TIMESYNC_0")
	assert.True(t, found, "ptpInputPin (ETH01_SDP_TIMESYNC_0) should exist in perla4 pins")
	if found {
		t.Logf("  ✓ PTP input pin: ETH01_SDP_TIMESYNC_0 (ID=%d)", ptpInputPin.ID)
	}

	gnssInputPin, found := cache.GetPin(actualClockID, "GNSS_1PPS_IN")
	assert.True(t, found, "gnssInputPin (GNSS_1PPS_IN) should exist in perla4 pins")
	if found {
		t.Logf("  ✓ GNSS input pin: GNSS_1PPS_IN (ID=%d)", gnssInputPin.ID)
	}

	// Load the minimal hardware config for dell/XR8720t
	hwConfig, err := loadHardwareConfigFromFile("testdata/gnrd-hwconfig-minimal-perla4.yaml")
	if !assert.NoError(t, err, "Failed to load gnrd-hwconfig-minimal-perla4.yaml") {
		return
	}
	assert.NotNil(t, hwConfig)
	assert.Equal(t, "dell/XR8720t", hwConfig.Spec.Profile.ClockChain.Structure[0].HardwareSpecificDefinitions,
		"Hardware specific definitions should reference dell/XR8720t")

	t.Logf("✓ Loaded hardware config: %s (clockType=%s, hwDef=%s)",
		hwConfig.Name,
		*hwConfig.Spec.Profile.ClockType,
		hwConfig.Spec.Profile.ClockChain.Structure[0].HardwareSpecificDefinitions)
}

func TestDellXR8720tTBCAppliesPackageLabeledPTPPin(t *testing.T) {
	mockGetter, err := CreateMockDpllPinsGetterFromFile("testdata/pins-perla4.json")
	if !assert.NoError(t, err) {
		t.FailNow()
	}
	SetDpllPinsGetter(mockGetter)
	defer TeardownMockDpllPinsForTests()

	cache, err := GetDpllPins()
	if !assert.NoError(t, err) {
		t.FailNow()
	}
	var clockID uint64
	for id := range cache.BoardLabelPins {
		clockID = id
		break
	}
	if !assert.NotZero(t, clockID) {
		t.FailNow()
	}

	hwConfig, err := loadHardwareConfigFromFile("testdata/gnrd-hwconfig-minimal-perla4.yaml")
	if !assert.NoError(t, err) || !assert.NotNil(t, hwConfig) {
		t.FailNow()
	}
	ptpConfig, err := loadPtpConfigFromFile("testdata/tbc-gnrd.yaml")
	if !assert.NoError(t, err) || !assert.NotNil(t, ptpConfig) {
		t.FailNow()
	}

	mockResolver := newMockLeadingInterfaceResolver()
	mockResolver.phcIDs["eno2"] = testDevPtp0
	mockResolver.symlinks["/sys/class/ptp/ptp0/device"] = "../../../0000:13:00.0"
	mockResolver.dirEntries["/sys/bus/pci/devices/0000:13:00.0/net"] = []os.DirEntry{
		&mockDirEntry{name: "eno5", isDir: false},
	}
	SetLeadingInterfaceResolver(mockResolver)
	defer ResetLeadingInterfaceResolver()
	SetupMockPtpDeviceResolverWithDevices(map[string][]string{
		"/sys/class/net/eno5/device/ptp/ptp*/pins/SDP0": {
			"/sys/class/net/eno5/device/ptp/ptp0/pins/SDP0",
		},
	})
	defer TeardownMockPtpDeviceResolver()

	hcm := newHardwareConfigManagerForTests()
	hcm.overrideExecutors(func([]dpll.PinParentDeviceCtl) error { return nil }, func(string, string) error { return nil })
	defer hcm.resetExecutors()
	hcm.pinCache = cache
	hcm.clockIDCache = map[string]uint64{"eno5:dell/XR8720t": clockID}
	resolved, err := hcm.ResolveClockChain(hwConfig, ptpConfig)
	if !assert.NoError(t, err) || !assert.NotNil(t, resolved) {
		t.FailNow()
	}

	var ptpLabel string
	for _, condition := range resolved.Spec.Profile.ClockChain.Behavior.Conditions {
		if condition.Name != testConditionInitializeTBC {
			continue
		}
		for _, desiredState := range condition.DesiredStates {
			if desiredState.DPLL != nil && desiredState.DPLL.BoardLabel == testPackageLabelREF0N {
				ptpLabel = desiredState.DPLL.BoardLabel
			}
		}
	}
	assert.Equal(t, testPackageLabelREF0N, ptpLabel)
	pin, found := cache.GetPin(clockID, ptpLabel)
	if !assert.True(t, found) {
		t.FailNow()
	}
	assert.Equal(t, testPackageLabelREF0N, pin.PackageLabel)
	assert.Equal(t, "ETH01_SDP_TIMESYNC_0", pin.BoardLabel)

	dpllCommands, sysfsCommands, err := hcm.resolveClockChainBehavior(*resolved)
	if !assert.NoError(t, err) {
		t.FailNow()
	}
	hcm.hardwareConfigs = []enrichedHardwareConfig{{
		HardwareConfig:  *resolved,
		dpllPinCommands: dpllCommands,
		sysFSCommands:   sysfsCommands,
	}}

	profile := &ptpv1.PtpProfile{Name: stringPtr("t-bc_01-tbc-tr")}
	assert.NoError(t, hcm.ApplyHardwareConfigsForProfile(profile))
}

// TestLoadBehaviorProfile_DellXR8720t tests that dell/XR8720t behavior profiles load correctly
func TestLoadBehaviorProfile_DellXR8720t(t *testing.T) {
	fakeClient := fake.NewClientset()
	loader := NewBoardLabelMapLoader(fakeClient, "default")

	template, err := LoadBehaviorProfile("dell/XR8720t", ClockTypeTBC, loader)
	assert.NoError(t, err, "Should load dell/XR8720t T-BC behavior profile")
	assert.NotNil(t, template, "Behavior template should not be nil")
	if template == nil {
		t.Fatal("template is nil")
	}

	// Verify pin roles
	assert.NotEmpty(t, template.PinRoles, "PinRoles should not be empty")
	assert.Equal(t, testPackageLabelREF0N, template.PinRoles["ptpInputPin"],
		"ptpInputPin should be REF0N for Dell XR8720t")
	assert.Equal(t, testPackageLabelREF4P, template.PinRoles["gnssInputPin"],
		"gnssInputPin should be REF4P for Dell XR8720t")

	// Verify sources template
	assert.NotEmpty(t, template.Sources, "Sources should not be empty")
	assert.Equal(t, "PTP", template.Sources[0].Name, "First source should be PTP")
	assert.Equal(t, ptpv2alpha1.SourceTypePTP, template.Sources[0].SourceType)

	// Verify conditions template
	assert.NotEmpty(t, template.Conditions, "Conditions should not be empty")

	expectedConditions := []string{testConditionInitializeTBC, "PTP Source Locked", "PTP Source Lost - Leader Holdover"}
	for _, expectedName := range expectedConditions {
		found := false
		for _, condition := range template.Conditions {
			if condition.Name == expectedName {
				found = true
				break
			}
		}
		assert.True(t, found, "Condition '%s' should be present", expectedName)
		if found {
			t.Logf("  ✓ Found condition: %s", expectedName)
		}
	}

	t.Logf("✓ Dell XR8720t behavior profile loaded: %d sources, %d conditions",
		len(template.Sources), len(template.Conditions))
}

// TestLoadBehaviorProfile_MultiVendor tests behavior profile loading for multiple vendors
func TestLoadBehaviorProfile_MultiVendor(t *testing.T) {
	fakeClient := fake.NewClientset()
	loader := NewBoardLabelMapLoader(fakeClient, "default")

	vendors := []struct {
		hwDefPath         string
		clockType         string
		expectedPtpInput  string
		expectedGnssInput string
	}{
		{
			hwDefPath:         HwDefIntelE825,
			clockType:         ClockTypeTBC,
			expectedPtpInput:  "GNR-D_SDP0",
			expectedGnssInput: "GNSS_1PPS_IN",
		},
		{
			hwDefPath:         "dell/XR8720t",
			clockType:         ClockTypeTBC,
			expectedPtpInput:  testPackageLabelREF0N,
			expectedGnssInput: testPackageLabelREF4P,
		},
	}

	for _, v := range vendors {
		t.Run(v.hwDefPath, func(t *testing.T) {
			template, err := LoadBehaviorProfile(v.hwDefPath, v.clockType, loader)
			assert.NoError(t, err, "Should load %s %s behavior profile", v.hwDefPath, v.clockType)
			if !assert.NotNil(t, template, "Behavior template should not be nil") {
				return
			}

			assert.Equal(t, v.expectedPtpInput, template.PinRoles["ptpInputPin"],
				"ptpInputPin mismatch for %s", v.hwDefPath)
			assert.Equal(t, v.expectedGnssInput, template.PinRoles["gnssInputPin"],
				"gnssInputPin mismatch for %s", v.hwDefPath)

			// Both vendors should have the same condition names
			expectedConditions := []string{testConditionInitializeTBC, "PTP Source Locked", "PTP Source Lost - Leader Holdover"}
			for _, name := range expectedConditions {
				found := false
				for _, c := range template.Conditions {
					if c.Name == name {
						found = true
						break
					}
				}
				assert.True(t, found, "%s: condition '%s' should be present", v.hwDefPath, name)
			}

			t.Logf("✓ %s: ptpInputPin=%s, gnssInputPin=%s, conditions=%d",
				v.hwDefPath, template.PinRoles["ptpInputPin"], template.PinRoles["gnssInputPin"], len(template.Conditions))
		})
	}
}

// TestLoadHardwareDefaults_MultiVendor validates that LoadHardwareDefaults succeeds for
// every embedded vendor, catching YAML parse errors (such as indentation issues) in
// defaults.yaml and delays.yaml.
func TestLoadHardwareDefaults_MultiVendor(t *testing.T) {
	vendors := []struct {
		hwDefPath       string
		expectDefaults  bool // true if defaults.yaml exists
		expectDelays    bool // true if delays.yaml exists
		expectedPinDefs int  // minimum number of pin defaults (0 means unchecked)
	}{
		{
			hwDefPath:       HwDefIntelE825,
			expectDefaults:  true,
			expectDelays:    true,
			expectedPinDefs: 1,
		},
		{
			hwDefPath:      "dell/XR8720t",
			expectDefaults: false, // no defaults.yaml for this hardware
			expectDelays:   true,
		},
	}

	for _, v := range vendors {
		t.Run(v.hwDefPath, func(t *testing.T) {
			hwSpec, err := LoadHardwareDefaults(v.hwDefPath, nil)
			assert.NoError(t, err, "LoadHardwareDefaults should not return error for %s", v.hwDefPath)

			if v.expectDefaults {
				assert.NotNil(t, hwSpec, "Hardware spec should not be nil for %s", v.hwDefPath)
				if hwSpec != nil && v.expectedPinDefs > 0 {
					assert.GreaterOrEqual(t, len(hwSpec.PinDefaults), v.expectedPinDefs,
						"%s: should have at least %d pin defaults", v.hwDefPath, v.expectedPinDefs)
				}
			}

			if v.expectDelays {
				// Delays are loaded as part of LoadHardwareDefaults; if YAML is malformed
				// the call above will have returned an error, so reaching here proves
				// the delays.yaml parsed successfully.
				t.Logf("✓ %s: delays.yaml parsed successfully", v.hwDefPath)
			}

			if hwSpec != nil {
				t.Logf("✓ %s: loaded (pinDefaults=%d, hasDelayCompensation=%v)",
					v.hwDefPath, len(hwSpec.PinDefaults), hwSpec.DelayCompensation != nil)
			} else {
				t.Logf("✓ %s: empty defaults (expected)", v.hwDefPath)
			}
		})
	}
}

// TestClockIDResolution tests clock ID resolution for E825 hardware
func TestClockIDResolution(t *testing.T) {
	// Load pins from perla2-pins.json to get expected clock ID
	mockGetter, err := CreateMockDpllPinsGetterFromFile("../daemon/testdata/perla2-pins.json")
	if !assert.NoError(t, err, "Failed to create mock getter from perla2-pins.json") {
		t.FailNow()
	}
	SetDpllPinsGetter(mockGetter)
	defer TeardownMockDpllPinsForTests()

	// Get the expected clock ID from the loaded pins
	cache, err := GetDpllPins()
	if !assert.NoError(t, err, "Failed to get DPLL pins") {
		return
	}

	var expectedClockID uint64
	for clockID := range cache.BoardLabelPins {
		expectedClockID = clockID
		t.Logf("Expected clock ID from pin cache: %#x", expectedClockID)
		break
	}

	// Setup mock command executor for PERLA hardware (E825 + zl3073x)
	mockCmd := NewMockCommandExecutor()

	// Mock ethtool to return bus address
	mockCmd.SetResponse("ethtool", []string{"-i", "eno5"}, "driver: ice\nbus-info: 0000:51:00.0")

	// Mock lspci to return E825 device (triggers PERLA workaround)
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E825-C for backplane")

	// Fallback serial number (won't be used due to PERLA workaround)
	serialNumber := fmt.Sprintf("%02x-%02x-%02x-%02x-%02x-%02x-%02x-%02x",
		(expectedClockID>>56)&0xff, (expectedClockID>>48)&0xff, (expectedClockID>>40)&0xff, (expectedClockID>>32)&0xff,
		(expectedClockID>>24)&0xff, (expectedClockID>>16)&0xff, (expectedClockID>>8)&0xff, expectedClockID&0xff)
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number "+serialNumber)

	SetCommandExecutor(mockCmd)
	defer ResetCommandExecutor()

	// Resolve clock ID
	clockID, err := GetClockIDFromInterface("eno5", HwDefIntelE825)
	if !assert.NoError(t, err, "Failed to resolve clock ID") {
		return
	}

	assert.Equal(t, expectedClockID, clockID, "Clock ID should match perla2-pins.json")
	t.Logf("✓ Clock ID resolved correctly: %#x", clockID)
}

func TestLoadBehaviorProfile_TGM(t *testing.T) {
	fakeClient := fake.NewClientset()
	loader := NewBoardLabelMapLoader(fakeClient, "default")

	tests := []struct {
		name           string
		hwDef          string
		gnssBoardLabel string
	}{
		{HwDefIntelE810, HwDefIntelE810, "GNSS-1PPS"},
		{HwDefIntelE825, HwDefIntelE825, "GNSS_1PPS_IN"},
		{HwDefDellXR8720t, HwDefDellXR8720t, testPackageLabelREF4P},
		{HwDefHPEEL140Gen12, HwDefHPEEL140Gen12, testPackageLabelREF4P},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			template, err := LoadBehaviorProfile(tt.hwDef, testClockTypeTGM, loader)
			assert.NoError(t, err)
			if !assert.NotNil(t, template, "T-GM behavior template should exist for %s", tt.name) {
				return
			}

			// Should have GNSS source
			assert.NotEmpty(t, template.Sources)
			assert.Equal(t, testSourceGNSS, template.Sources[0].Name)
			assert.Equal(t, ptpv2alpha1.SourceTypeGNSS, template.Sources[0].SourceType)
			assert.Equal(t, tt.gnssBoardLabel, template.Sources[0].BoardLabel)

			// Should have init condition
			assert.NotEmpty(t, template.Conditions)
			assert.Equal(t, "Initialize T-GM", template.Conditions[0].Name)

			t.Logf("✓ %s T-GM profile: %d sources, %d conditions",
				tt.name, len(template.Sources), len(template.Conditions))
		})
	}
}

func TestHPEEL140UsesPackageLabelsInBehaviorProfiles(t *testing.T) {
	fakeClient := fake.NewClientset()
	loader := NewBoardLabelMapLoader(fakeClient, "default")

	tgm, err := LoadBehaviorProfile(HwDefHPEEL140Gen12, testClockTypeTGM, loader)
	if !assert.NoError(t, err) || !assert.NotNil(t, tgm) {
		t.FailNow()
	}
	assert.Equal(t, testPackageLabelREF4P, tgm.Sources[0].BoardLabel)
	assert.Equal(t, testPackageLabelREF4P, tgm.Conditions[0].DesiredStates[0].DPLL.BoardLabel)
	assert.Equal(t, "REF2N", tgm.Conditions[0].DesiredStates[1].DPLL.BoardLabel)

	tbc, err := LoadBehaviorProfile(HwDefHPEEL140Gen12, ClockTypeTBC, loader)
	if !assert.NoError(t, err) || !assert.NotNil(t, tbc) {
		t.FailNow()
	}
	assert.Equal(t, testPackageLabelREF4P, tbc.PinRoles["gnssInputPin"])
	assert.Equal(t, testPackageLabelREF0N, tbc.PinRoles["ptpInputPin"])

	labels := make(map[string]bool)
	for _, condition := range tbc.Conditions {
		for _, desiredState := range condition.DesiredStates {
			if desiredState.DPLL != nil {
				labels[desiredState.DPLL.BoardLabel] = true
			}
		}
	}
	assert.True(t, labels["{gnssInputPin}"])
	assert.True(t, labels["{ptpInputPin}"])
	assert.True(t, labels["REF2N"])
	assert.True(t, labels["REF0P"])
}

func TestDeriveBehavior_MergesUserGNSSConfig(t *testing.T) {
	fakeClient := fake.NewClientset()
	hcm := NewHardwareConfigManager(fakeClient, "default", nil)

	clockType := testClockTypeTGM
	subsystemName := testSubsystemLeader

	tests := []struct {
		name               string
		hwConfig           *ptpv2alpha1.HardwareConfig
		expectedBoardLabel string
		expectedGnssConfig *ptpv2alpha1.GNSSConfig
	}{
		{
			name: "No user override",
			// User provides only gnssConfig on the GNSS source — the template
			// provides the DPLL pin details and conditions.
			hwConfig: &ptpv2alpha1.HardwareConfig{
				Spec: ptpv2alpha1.HardwareConfigSpec{
					Profile: ptpv2alpha1.HardwareProfile{
						ClockType: &clockType,
						ClockChain: &ptpv2alpha1.ClockChain{
							Structure: []ptpv2alpha1.Subsystem{
								{
									Name:                        subsystemName,
									HardwareSpecificDefinitions: HwDefDellXR8720t,
									DPLL: ptpv2alpha1.DPLL{
										NetworkInterface: testIfaceEno8703,
									},
									Ethernet: []ptpv2alpha1.Ethernet{
										{Ports: []string{testIfaceEno8703}},
									},
								},
							},
							Behavior: &ptpv2alpha1.Behavior{
								Sources: []ptpv2alpha1.SourceConfig{},
							},
						},
					},
				},
			},
			expectedBoardLabel: testPackageLabelREF4P,
			expectedGnssConfig: &ptpv2alpha1.GNSSConfig{
				Init: ptpv2alpha1.GNSSInit{},
				Match: &ptpv2alpha1.GNSSMatcher{
					TTYDevice: testACM0,
				},
			},
		},
		{
			name: "User override of init only",
			// User provides only gnssConfig on the GNSS source — the template
			// provides the DPLL pin details and conditions.
			hwConfig: &ptpv2alpha1.HardwareConfig{
				Spec: ptpv2alpha1.HardwareConfigSpec{
					Profile: ptpv2alpha1.HardwareProfile{
						ClockType: &clockType,
						ClockChain: &ptpv2alpha1.ClockChain{
							Structure: []ptpv2alpha1.Subsystem{
								{
									Name:                        subsystemName,
									HardwareSpecificDefinitions: HwDefDellXR8720t,
									DPLL: ptpv2alpha1.DPLL{
										NetworkInterface: testIfaceEno8703,
									},
									Ethernet: []ptpv2alpha1.Ethernet{
										{Ports: []string{testIfaceEno8703}},
									},
								},
							},
							Behavior: &ptpv2alpha1.Behavior{
								Sources: []ptpv2alpha1.SourceConfig{
									{
										Name:       testSourceGNSS,
										SourceType: ptpv2alpha1.SourceTypeGNSS,
										GNSSConfig: &ptpv2alpha1.GNSSConfig{
											Init: ptpv2alpha1.GNSSInit{
												AntennaVoltage: true,
												Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
												SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
													ObservationTime: 600,
													Accuracy:        5,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedBoardLabel: testPackageLabelREF4P,
			expectedGnssConfig: &ptpv2alpha1.GNSSConfig{
				Init: ptpv2alpha1.GNSSInit{
					AntennaVoltage: true,
					Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
					SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
						ObservationTime: 600,
						Accuracy:        5,
					},
				},
				Match: &ptpv2alpha1.GNSSMatcher{
					TTYDevice: testACM0,
				},
			},
		},
		{
			name: "Full user override",
			// User provides only gnssConfig on the GNSS source — the template
			// provides the DPLL pin details and conditions.
			hwConfig: &ptpv2alpha1.HardwareConfig{
				Spec: ptpv2alpha1.HardwareConfigSpec{
					Profile: ptpv2alpha1.HardwareProfile{
						ClockType: &clockType,
						ClockChain: &ptpv2alpha1.ClockChain{
							Structure: []ptpv2alpha1.Subsystem{
								{
									Name:                        subsystemName,
									HardwareSpecificDefinitions: HwDefDellXR8720t,
									DPLL: ptpv2alpha1.DPLL{
										NetworkInterface: testIfaceEno8703,
									},
									Ethernet: []ptpv2alpha1.Ethernet{
										{Ports: []string{testIfaceEno8703}},
									},
								},
							},
							Behavior: &ptpv2alpha1.Behavior{
								Sources: []ptpv2alpha1.SourceConfig{
									{
										Name:       testSourceGNSS,
										SourceType: ptpv2alpha1.SourceTypeGNSS,
										GNSSConfig: &ptpv2alpha1.GNSSConfig{
											Init: ptpv2alpha1.GNSSInit{
												AntennaVoltage: true,
												Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
												SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
													ObservationTime: 600,
													Accuracy:        5,
												},
											},
											Match: &ptpv2alpha1.GNSSMatcher{
												EthernetInterface: testIfaceEno8703,
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedBoardLabel: testPackageLabelREF4P,
			expectedGnssConfig: &ptpv2alpha1.GNSSConfig{
				Init: ptpv2alpha1.GNSSInit{
					AntennaVoltage: true,
					Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
					SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
						ObservationTime: 600,
						Accuracy:        5,
					},
				},
				Match: &ptpv2alpha1.GNSSMatcher{
					EthernetInterface: testIfaceEno8703,
				},
			},
		},
		{
			name: "User override of init only (no default matcher)",
			// User provides only gnssConfig on the GNSS source — the template
			// provides the DPLL pin details and conditions.
			hwConfig: &ptpv2alpha1.HardwareConfig{
				Spec: ptpv2alpha1.HardwareConfigSpec{
					Profile: ptpv2alpha1.HardwareProfile{
						ClockType: &clockType,
						ClockChain: &ptpv2alpha1.ClockChain{
							Structure: []ptpv2alpha1.Subsystem{
								{
									Name:                        subsystemName,
									HardwareSpecificDefinitions: HwDefIntelE810,
									DPLL: ptpv2alpha1.DPLL{
										NetworkInterface: testIfaceEno8703,
									},
									Ethernet: []ptpv2alpha1.Ethernet{
										{Ports: []string{testIfaceEno8703}},
									},
								},
							},
							Behavior: &ptpv2alpha1.Behavior{
								Sources: []ptpv2alpha1.SourceConfig{
									{
										Name:       testSourceGNSS,
										SourceType: ptpv2alpha1.SourceTypeGNSS,
										GNSSConfig: &ptpv2alpha1.GNSSConfig{
											Init: ptpv2alpha1.GNSSInit{
												AntennaVoltage: true,
												Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
												SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
													ObservationTime: 600,
													Accuracy:        5,
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedBoardLabel: "GNSS-1PPS",
			expectedGnssConfig: &ptpv2alpha1.GNSSConfig{
				Init: ptpv2alpha1.GNSSInit{
					AntennaVoltage: true,
					Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
					SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
						ObservationTime: 600,
						Accuracy:        5,
					},
				},
				Match: nil,
			},
		},
		{
			name: "Full user override (no default matcher)",
			// User provides only gnssConfig on the GNSS source — the template
			// provides the DPLL pin details and conditions.
			hwConfig: &ptpv2alpha1.HardwareConfig{
				Spec: ptpv2alpha1.HardwareConfigSpec{
					Profile: ptpv2alpha1.HardwareProfile{
						ClockType: &clockType,
						ClockChain: &ptpv2alpha1.ClockChain{
							Structure: []ptpv2alpha1.Subsystem{
								{
									Name:                        subsystemName,
									HardwareSpecificDefinitions: HwDefIntelE810,
									DPLL: ptpv2alpha1.DPLL{
										NetworkInterface: testIfaceEno8703,
									},
									Ethernet: []ptpv2alpha1.Ethernet{
										{Ports: []string{testIfaceEno8703}},
									},
								},
							},
							Behavior: &ptpv2alpha1.Behavior{
								Sources: []ptpv2alpha1.SourceConfig{
									{
										Name:       testSourceGNSS,
										SourceType: ptpv2alpha1.SourceTypeGNSS,
										GNSSConfig: &ptpv2alpha1.GNSSConfig{
											Init: ptpv2alpha1.GNSSInit{
												AntennaVoltage: true,
												Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
												SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
													ObservationTime: 600,
													Accuracy:        5,
												},
											},
											Match: &ptpv2alpha1.GNSSMatcher{
												EthernetInterface: testIfaceEno8703,
											},
										},
									},
								},
							},
						},
					},
				},
			},
			expectedBoardLabel: "GNSS-1PPS",
			expectedGnssConfig: &ptpv2alpha1.GNSSConfig{
				Init: ptpv2alpha1.GNSSInit{
					AntennaVoltage: true,
					Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
					SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
						ObservationTime: 600,
						Accuracy:        5,
					},
				},
				Match: &ptpv2alpha1.GNSSMatcher{
					EthernetInterface: testIfaceEno8703,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := hcm.deriveBehavior(tt.hwConfig, clockType)
			assert.NoError(t, err)

			behavior := tt.hwConfig.Spec.Profile.ClockChain.Behavior
			assert.NotNil(t, behavior)

			// Should have the GNSS source from template with user's gnssConfig merged
			var gnssSource *ptpv2alpha1.SourceConfig
			for i, s := range behavior.Sources {
				if s.SourceType == ptpv2alpha1.SourceTypeGNSS {
					gnssSource = &behavior.Sources[i]
					break
				}
			}
			if !assert.NotNil(t, gnssSource, "GNSS source should exist") {
				return
			}

			// Template fields should be resolved
			assert.Equal(t, subsystemName, gnssSource.Subsystem, "subsystem should come from template resolution")
			assert.Equal(t, tt.expectedBoardLabel, gnssSource.BoardLabel, "boardLabel should come from template")

			// User fields should be merged
			assert.NotNil(t, gnssSource.GNSSConfig, "gnssConfig should be merged from user")
			assert.Equal(t, tt.expectedGnssConfig, gnssSource.GNSSConfig, "gnssConfig should match expected merged value")

			// Conditions should come from template
			assert.NotEmpty(t, behavior.Conditions)
			assert.Equal(t, "Initialize T-GM", behavior.Conditions[0].Name)

			t.Logf("✓ Merge successful: GNSS source has template pin details + user gnssConfig")
			t.Logf("  Sources: %d, Conditions: %d", len(behavior.Sources), len(behavior.Conditions))
		})
	}
}
