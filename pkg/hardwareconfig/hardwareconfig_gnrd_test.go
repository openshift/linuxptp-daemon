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
		Name: stringPtr("01-tbc-tr"),
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
	for clockID := range cache.Pins {
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
	hwSpec, err := LoadHardwareDefaults("intel/e825", nil)
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

	hwSpec, err := LoadHardwareDefaults("intel/e825", nil)
	if !assert.NoError(t, err, "Failed to load e825 defaults") {
		return
	}
	if !assert.NotNil(t, hwSpec, "Hardware spec should not be nil") {
		return
	}

	// Validate key fields from e825/defaults.yaml
	assert.NotNil(t, hwSpec.ClockIDTransformation, "Clock ID transformation should be defined")
	assert.Equal(t, "direct", hwSpec.ClockIDTransformation.Method, "Should use direct transformation (based on actual PERLA2 hardware)")
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
				for clkID, pins := range cache.Pins {
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
	for clockID := range cache.Pins {
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
	clockID, err := GetClockIDFromInterface("eno5", "intel/e825")
	if !assert.NoError(t, err, "Failed to resolve clock ID") {
		return
	}

	assert.Equal(t, expectedClockID, clockID, "Clock ID should match perla2-pins.json")
	t.Logf("✓ Clock ID resolved correctly: %#x", clockID)
}
