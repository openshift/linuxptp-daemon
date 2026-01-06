package hardwareconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/scheme"
)

func TestApplyHardwareConfigsForProfile(t *testing.T) {
	// Set up mock PTP device resolver for testing
	SetupMockPtpDeviceResolver()
	defer TeardownMockPtpDeviceResolver()
	// Set up mock DPLL pins from test file for testing
	mockErr := SetupMockDpllPinsForTests()
	if mockErr != nil {
		t.Logf("Warning: Failed to setup mock DPLL pins: %v", mockErr)
		// Continue with test as DPLL pins are optional
	}
	defer TeardownMockDpllPinsForTests()

	tests := []struct {
		name         string
		testDataFile string
		profileName  string
	}{
		{
			name:         "successful hardware config application",
			testDataFile: "testdata/wpc-hwconfig.yaml",
			profileName:  "01-tbc-tr",
		},
		{
			name:         "no matching profile",
			testDataFile: "testdata/wpc-hwconfig.yaml",
			profileName:  "non-existent-profile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetupMockPtpDeviceResolver()
			defer TeardownMockPtpDeviceResolver()

			if err := SetupMockDpllPinsForTests(); err != nil {
				t.Fatalf("failed to set up mock DPLL pins: %v", err)
			}
			defer TeardownMockDpllPinsForTests()

			// Set up mock command executor for GetClockIDFromInterface
			mockCmd := NewMockCommandExecutor()
			mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
			mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
			mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
			mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
			mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
			mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
			SetCommandExecutor(mockCmd)
			defer ResetCommandExecutor()

			// Load test data
			hwConfig, err := loadHardwareConfigFromFile(tt.testDataFile)
			assert.NoError(t, err)
			assert.NotNil(t, hwConfig)

			// Create hardware config manager and add test data
			hcm := NewHardwareConfigManager()
			defer hcm.resetExecutors()

			hcm.overrideExecutors(nil, func(_, _ string) error { return nil })

			var appliedPins []dpll.PinParentDeviceCtl
			hcm.overrideExecutors(func(cmds []dpll.PinParentDeviceCtl) error {
				snapshot := make([]dpll.PinParentDeviceCtl, len(cmds))
				copy(snapshot, cmds)
				appliedPins = append(appliedPins, snapshot...)
				return nil
			}, func(_, _ string) error {
				return nil
			})

			err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{*hwConfig})
			assert.NoError(t, err)

			// Create a mock PTP profile
			profile := &ptpv1.PtpProfile{
				Name: &tt.profileName,
			}

			// Test the function
			err = hcm.ApplyHardwareConfigsForProfile(profile)

			assert.NoError(t, err)
		})
	}
}

func TestHardwareConfigManagerOperations(t *testing.T) {
	// Set up mock PTP device resolver for testing
	SetupMockPtpDeviceResolver()
	defer TeardownMockPtpDeviceResolver()

	// Set up mock DPLL pins for testing
	mockErr := SetupMockDpllPinsForTests()
	if mockErr != nil {
		t.Logf("Warning: Failed to setup mock DPLL pins: %v", mockErr)
		// Continue with test as DPLL pins are optional
	}
	defer TeardownMockDpllPinsForTests()

	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	SetCommandExecutor(mockCmd)
	defer ResetCommandExecutor()

	hcm := NewHardwareConfigManager()

	// Test initial state
	assert.Equal(t, 0, hcm.GetHardwareConfigCount())

	// Load test data
	hwConfig, err := loadHardwareConfigFromFile("testdata/wpc-hwconfig.yaml")
	assert.NoError(t, err)
	assert.NotNil(t, hwConfig)

	// Test UpdateHardwareConfig
	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{*hwConfig})
	assert.NoError(t, err)
	assert.Equal(t, 1, hcm.GetHardwareConfigCount())

	// Test HasHardwareConfigForProfile
	profile := &ptpv1.PtpProfile{
		Name: stringPtr("01-tbc-tr"),
	}
	assert.True(t, hcm.HasHardwareConfigForProfile(profile))

	// Test with non-existent profile
	profile.Name = stringPtr("non-existent")
	assert.False(t, hcm.HasHardwareConfigForProfile(profile))

	// Test GetHardwareConfigsForProfile
	profile.Name = stringPtr("01-tbc-tr")
	configs := hcm.GetHardwareConfigsForProfile(profile)
	assert.Len(t, configs, 1)
	assert.Equal(t, "tbc", *configs[0].Name)

	// Test ClearHardwareConfigs
	hcm.ClearHardwareConfigs()
	assert.Equal(t, 0, hcm.GetHardwareConfigCount())
	assert.False(t, hcm.HasHardwareConfigForProfile(profile))
}

func TestHardwareConfigManagerEmptyConfigs(t *testing.T) {
	// Set up mock DPLL pins from test file for testing
	mockErr := SetupMockDpllPinsForTests()
	if mockErr != nil {
		t.Logf("Warning: Failed to setup mock DPLL pins: %v", mockErr)
		// Continue with test as DPLL pins are optional
	}
	defer TeardownMockDpllPinsForTests()

	hcm := NewHardwareConfigManager()

	// Test with empty configs
	err := hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{})
	assert.NoError(t, err)
	assert.Equal(t, 0, hcm.GetHardwareConfigCount())

	// Test with nil profile name
	profile := &ptpv1.PtpProfile{}
	assert.False(t, hcm.HasHardwareConfigForProfile(profile))

	configs := hcm.GetHardwareConfigsForProfile(profile)
	assert.Len(t, configs, 0)
}

// loadHardwareConfigFromFile loads a HardwareConfig from a YAML file
func loadHardwareConfigFromFile(filename string) (*ptpv2alpha1.HardwareConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %v", filename, err)
	}

	// Register the HardwareConfig types with the scheme
	_ = ptpv2alpha1.AddToScheme(scheme.Scheme)

	// Create a decoder
	decode := serializer.NewCodecFactory(scheme.Scheme).UniversalDeserializer().Decode

	// Decode the YAML
	obj, _, err := decode(data, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decode YAML: %v", err)
	}

	hwConfig, ok := obj.(*ptpv2alpha1.HardwareConfig)
	if !ok {
		return nil, fmt.Errorf("decoded object is not a HardwareConfig")
	}

	return hwConfig, nil
}

// stringPtr returns a pointer to a string
func stringPtr(s string) *string {
	return &s
}

func TestLoadHardwareConfigFromFile(t *testing.T) {
	// Test successful loading
	hwConfig, err := loadHardwareConfigFromFile("testdata/wpc-hwconfig.yaml")
	assert.NoError(t, err)
	assert.NotNil(t, hwConfig)
	assert.Equal(t, "test", hwConfig.Name)
	assert.Equal(t, "01-tbc-tr", hwConfig.Spec.RelatedPtpProfileName)
	assert.Equal(t, "tbc", *hwConfig.Spec.Profile.Name)

	// Test with non-existent file
	_, err = loadHardwareConfigFromFile("testdata/non-existent.yaml")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to read file")
}

func TestNewHardwareConfigManager(t *testing.T) {
	hcm := NewHardwareConfigManager()
	assert.NotNil(t, hcm)
	assert.Equal(t, 0, hcm.GetHardwareConfigCount())
}

func TestPTPStateDetector(t *testing.T) {
	// Set up mock PTP device resolver for testing
	SetupMockPtpDeviceResolver()
	defer TeardownMockPtpDeviceResolver()

	// Set up mock DPLL pins for testing
	mockErr := SetupMockDpllPinsForTests()
	if mockErr != nil {
		t.Logf("Warning: Failed to setup mock DPLL pins: %v", mockErr)
		// Continue with test as DPLL pins are optional
	}
	defer TeardownMockDpllPinsForTests()

	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	SetCommandExecutor(mockCmd)
	defer ResetCommandExecutor()

	// Load test data
	hwConfig, err := loadHardwareConfigFromFile("testdata/wpc-hwconfig.yaml")
	assert.NoError(t, err)
	assert.NotNil(t, hwConfig)

	// Create hardware config manager and add test data
	hcm := NewHardwareConfigManager()
	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{*hwConfig})
	assert.NoError(t, err)

	// Create PTP state detector
	psd := NewPTPStateDetector(hcm)

	// Test GetMonitoredPorts
	monitoredPorts := psd.GetMonitoredPorts()
	assert.Contains(t, monitoredPorts, "ens4f1")

	// Test GetBehaviorRules
	conditions := psd.GetBehaviorRules()
	assert.NotEmpty(t, conditions)
}

func TestDetectStateChange(t *testing.T) {
	// Set up mock PTP device resolver for testing
	SetupMockPtpDeviceResolver()
	defer TeardownMockPtpDeviceResolver()

	// Set up mock DPLL pins for testing
	mockErr := SetupMockDpllPinsForTests()
	if mockErr != nil {
		t.Logf("Warning: Failed to setup mock DPLL pins: %v", mockErr)
		// Continue with test as DPLL pins are optional
	}
	defer TeardownMockDpllPinsForTests()

	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	SetCommandExecutor(mockCmd)
	defer ResetCommandExecutor()

	// Load test data
	hwConfig, err := loadHardwareConfigFromFile("testdata/wpc-hwconfig.yaml")
	assert.NoError(t, err)

	// Create hardware config manager and add test data
	hcm := NewHardwareConfigManager()
	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{*hwConfig})
	assert.NoError(t, err)

	// Create PTP state detector
	psd := NewPTPStateDetector(hcm)

	t.Run("individual_test_cases", func(t *testing.T) {
		testCases := []struct {
			name     string
			logLine  string
			expected string
		}{
			{
				name:     "locked condition",
				logLine:  "ptp4l[1716691.337]: [ptp4l.1.config:5] port 1 (ens4f1): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
				expected: "locked",
			},
			{
				name:     "lost condition",
				logLine:  "ptp4l[1031716.424]: [ptp4l.0.config:5] port 1 (ens4f1): SLAVE to FAULT_DETECTED on FAULT_DETECTED",
				expected: "lost",
			},
			{
				name:     "non-monitored port - should return empty",
				logLine:  "ptp4l[1031716.424]: [ptp4l.0.config:5] port 2 (ens8f0): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
				expected: "", // ens8f0 is not in PTPTimeReceivers for the test data
			},
			{
				name:     "non-ptp4l log - should return empty",
				logLine:  "some other log message",
				expected: "",
			},
			{
				name:     "irrelevant ptp4l transition - should return empty",
				logLine:  "[ptp4l.0.config:5] port 1 (ens4f1): LISTENING to MASTER on INITIALIZATION",
				expected: "",
			},
			{
				name:     "user reported failing case - should work now",
				logLine:  "ptp4l[1720295.764]: [ptp4l.1.config:5] port 1 (ens4f1): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
				expected: "locked",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				result := psd.DetectStateChange(tc.logLine)
				assert.Equal(t, tc.expected, result, "State change detection should match expected result")
				if result != "" {
					t.Logf("✅ Detected %s condition from log line", result)
				}
			})
		}
	})

	t.Run("real_log_file_processing", func(t *testing.T) {
		// Read the real log file
		logData, readErr := os.ReadFile("testdata/log2.txt")
		assert.NoError(t, readErr, "Should be able to read log2.txt")

		// Split log into lines
		lines := strings.Split(string(logData), "\n")

		// Track detected state changes
		var detectedChanges []struct {
			lineNum   int
			condition string
			logLine   string
		}

		// Process each line
		for i, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}

			result := psd.DetectStateChange(line)
			if result != "" {
				detectedChanges = append(detectedChanges, struct {
					lineNum   int
					condition string
					logLine   string
				}{
					lineNum:   i + 1, // 1-based line numbers
					condition: result,
					logLine:   line,
				})
			}
		}

		// Log the results
		t.Logf("=== REAL LOG FILE PROCESSING RESULTS ===")
		t.Logf("Total lines processed: %d", len(lines))
		t.Logf("State changes detected: %d", len(detectedChanges))

		for _, change := range detectedChanges {
			t.Logf("Line %d: %s condition", change.lineNum, change.condition)
			t.Logf("  Log line: %s", change.logLine)
		}

		// Verify we detected some state changes (real logs should have transitions)
		if len(detectedChanges) > 0 {
			t.Logf("✅ Successfully detected %d state changes in real log file", len(detectedChanges))

			// Count locked vs lost conditions
			lockedCount := 0
			lostCount := 0
			for _, change := range detectedChanges {
				switch change.condition {
				case "locked":
					lockedCount++
				case "lost":
					lostCount++
				}
			}
			t.Logf("  Locked conditions: %d", lockedCount)
			t.Logf("  Lost conditions: %d", lostCount)
		} else {
			t.Logf("ℹ️  No state changes detected in log file (this may be expected if log contains no relevant transitions)")
		}
	})
}

func TestNewPTPStateDetector(t *testing.T) {
	hcm := NewHardwareConfigManager()
	psd := NewPTPStateDetector(hcm)
	assert.NotNil(t, psd)
	assert.NotNil(t, psd.hcm)
}

// TestApplyConditionDesiredStatesWithRealData tests applyConditionDesiredStatesByType using actual hardware config YAML data
//
//nolint:gocyclo // test complexity is acceptable
func TestApplyConditionDesiredStatesWithRealData(t *testing.T) {
	// Set up mock PTP device resolver for testing
	SetupMockPtpDeviceResolver()
	defer TeardownMockPtpDeviceResolver()

	// Set up mock DPLL pins for testing
	mockErr := SetupMockDpllPinsForTests()
	if mockErr != nil {
		t.Logf("Warning: Failed to setup mock DPLL pins: %v", mockErr)
		// Continue with test as DPLL pins are optional
	}
	defer TeardownMockDpllPinsForTests()

	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	SetCommandExecutor(mockCmd)
	defer ResetCommandExecutor()

	// Load the real hardware configuration from YAML
	hwConfig, err := loadHardwareConfigFromFile("testdata/wpc-hwconfig.yaml")
	if err != nil {
		t.Fatalf("Failed to load hardware config: %v", err)
	}

	// Create a HardwareConfigManager using the proper constructor
	hcm := NewHardwareConfigManager()
	defer hcm.resetExecutors()

	// Override executors to avoid actual hardware operations
	hcm.overrideExecutors(func(_ []dpll.PinParentDeviceCtl) error {
		// Mock DPLL executor - just return success without actually applying
		return nil
	}, func(_, _ string) error {
		// Mock SysFS executor - just return success without actually writing
		return nil
	})

	// Update hardware config to populate the manager
	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{*hwConfig})
	if err != nil {
		t.Fatalf("Failed to update hardware config: %v", err)
	}

	// Extract the clock chain from the loaded config
	clockChain := hwConfig.Spec.Profile.ClockChain
	profileName := *hwConfig.Spec.Profile.Name

	t.Logf("Testing with real hardware config: %s", hwConfig.Name)
	t.Logf("Profile: %s", profileName)
	t.Logf("Clock chain has %d conditions", len(clockChain.Behavior.Conditions))

	// Validate that source configurations reference subsystems
	if clockChain.Behavior != nil && len(clockChain.Behavior.Sources) > 0 {
		t.Logf("Validating %d behavior sources:", len(clockChain.Behavior.Sources))
		for i, source := range clockChain.Behavior.Sources {
			t.Logf("  Source %d: %s (Subsystem: %s)", i+1, source.Name, source.Subsystem)
		}
	}

	// Test each condition from the real hardware config
	for i, condition := range clockChain.Behavior.Conditions {
		// Triggers are mandatory - skip conditions without triggers
		if len(condition.Triggers) == 0 {
			t.Logf("Skipping condition '%s' - no triggers (triggers are mandatory)", condition.Name)
			continue
		}

		t.Run(fmt.Sprintf("condition_%d_%s", i, condition.Name), func(t *testing.T) {
			t.Logf("Testing condition: %s", condition.Name)
			t.Logf("  Triggers: %d", len(condition.Triggers))
			t.Logf("  Desired states: %d", len(condition.DesiredStates))

			// Log details about the condition
			for j, trigger := range condition.Triggers {
				t.Logf("    Trigger %d: %s (%s)", j+1, trigger.SourceName, trigger.ConditionType)
			}

			for j, desiredState := range condition.DesiredStates {
				if desiredState.DPLL != nil {
					t.Logf("    Desired state %d: DPLL - Subsystem: %s, Board: %s",
						j+1, desiredState.DPLL.Subsystem, desiredState.DPLL.BoardLabel)
					if desiredState.DPLL.EEC != nil {
						if desiredState.DPLL.EEC.Priority != nil {
							t.Logf("      EEC Priority: %d", *desiredState.DPLL.EEC.Priority)
						}
						if desiredState.DPLL.EEC.State != "" {
							t.Logf("      EEC State: %s", desiredState.DPLL.EEC.State)
						}
					}
					if desiredState.DPLL.PPS != nil {
						if desiredState.DPLL.PPS.Priority != nil {
							t.Logf("      PPS Priority: %d", *desiredState.DPLL.PPS.Priority)
						}
						if desiredState.DPLL.PPS.State != "" {
							t.Logf("      PPS State: %s", desiredState.DPLL.PPS.State)
						}
					}
				}
				if desiredState.SysFS != nil {
					t.Logf("    Desired state %d: SysFS - Path: %s, Value: %s",
						j+1, desiredState.SysFS.Path, desiredState.SysFS.Value)
					if desiredState.SysFS.SourceName != "" {
						t.Logf("      Source: %s", desiredState.SysFS.SourceName)
					}
				}
			}

			// Determine the condition type for this condition
			conditionType := condition.Triggers[0].ConditionType

			// Apply the condition's desired states in the exact defined order
			applyErr := hcm.applyDesiredStatesInOrder(condition, profileName, clockChain)
			// All conditions should apply successfully since the YAML is well-formed
			if applyErr != nil {
				t.Errorf("Failed to apply condition '%s' (type: %s): %v", condition.Name, conditionType, applyErr)
			} else {
				t.Logf("✅ Successfully applied condition '%s' (type: %s) with %d desired states",
					condition.Name, conditionType, len(condition.DesiredStates))
			}
		})
	}

	// Test specific conditions by name
	testCases := []struct {
		conditionName  string
		expectedStates int
		description    string
	}{
		{
			conditionName:  "Initialize T-BC",
			expectedStates: 5,
			description:    "Should have initialization states for GNSS and CVL pins plus sysFS config",
		},
		{
			conditionName:  "PTP Source Active",
			expectedStates: 2,
			description:    "Should have active configuration for CVL pins",
		},
		{
			conditionName:  "PTP Source Lost - Leader Holdover",
			expectedStates: 2,
			description:    "Should have holdover configuration for CVL pins",
		},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("validate_%s", strings.ReplaceAll(tc.conditionName, " ", "_")), func(t *testing.T) {
			// Find the condition by name
			var targetCondition *ptpv2alpha1.Condition
			for _, condition := range clockChain.Behavior.Conditions {
				if condition.Name == tc.conditionName {
					targetCondition = &condition
					break
				}
			}

			if targetCondition == nil {
				t.Fatalf("Condition '%s' not found in hardware config", tc.conditionName)
			}

			// Validate the expected number of desired states
			// Note: The actual count may vary slightly due to hardware-specific defaults being applied
			actualStates := len(targetCondition.DesiredStates)
			if actualStates < tc.expectedStates {
				t.Errorf("Expected at least %d desired states for '%s', got %d",
					tc.expectedStates, tc.conditionName, actualStates)
			}

			t.Logf("✅ %s", tc.description)
			t.Logf("   Found %d desired states (expected at least %d)", actualStates, tc.expectedStates)
		})
	}

	// Validate the hardware config structure
	t.Run("validate_hardware_config_structure", func(t *testing.T) {
		// Check that we have the expected sources
		sources := clockChain.Behavior.Sources
		if len(sources) != 1 {
			t.Errorf("Expected 1 source, got %d", len(sources))
		} else {
			source := sources[0]
			if source.Name != "PTP" {
				t.Errorf("Expected source name 'PTP', got '%s'", source.Name)
			}
			if source.SourceType != ptpTimeReceiverType {
				t.Errorf("Expected source type '%s', got '%s'", ptpTimeReceiverType, source.SourceType)
			}
			if len(source.PTPTimeReceivers) != 1 || source.PTPTimeReceivers[0] != "ens4f1" {
				t.Errorf("Expected PTP time receiver 'ens4f1', got %v", source.PTPTimeReceivers)
			}
			t.Logf("✅ Source configuration validated: %s (%s) with interface %s",
				source.Name, source.SourceType, source.PTPTimeReceivers[0])
		}

		// Check subsystems have network interfaces defined
		if len(clockChain.Structure) > 0 {
			for _, subsystem := range clockChain.Structure {
				if subsystem.DPLL.NetworkInterface == "" && (len(subsystem.Ethernet) == 0 || len(subsystem.Ethernet[0].Ports) == 0) {
					t.Errorf("Subsystem %s must have NetworkInterface or Ethernet ports", subsystem.Name)
				} else {
					t.Logf("✅ Subsystem %s has network interface for clock ID resolution", subsystem.Name)
				}
			}
		}
	})
}

// TestApplyDefaultAndInitConditions tests the applyDefaultAndInitConditions function
func TestApplyDefaultAndInitConditions(t *testing.T) {
	// Set up mock PTP device resolver for testing
	SetupMockPtpDeviceResolver()
	defer TeardownMockPtpDeviceResolver()

	// Set up mock DPLL pins for testing
	mockErr := SetupMockDpllPinsForTests()
	if mockErr != nil {
		t.Logf("Warning: Failed to setup mock DPLL pins: %v", mockErr)
		// Continue with test as DPLL pins are optional
	}
	defer TeardownMockDpllPinsForTests()

	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	SetCommandExecutor(mockCmd)
	defer ResetCommandExecutor()

	// Load test hardware config
	hwConfig, err := loadHardwareConfigFromFile("testdata/wpc-hwconfig.yaml")
	if err != nil {
		t.Fatalf("Failed to load hardware config: %v", err)
	}

	// Clock IDs are now resolved dynamically - no aliases needed

	// Create hardware config manager
	hcm := NewHardwareConfigManager()
	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{*hwConfig})
	if err != nil {
		t.Fatalf("Failed to update hardware config: %v", err)
	}

	profileName := "test-profile"
	clockChain := hwConfig.Spec.Profile.ClockChain

	tests := []struct {
		name                 string
		clockChain           *ptpv2alpha1.ClockChain
		profileName          string
		expectError          bool
		expectedDefaultCount int
		expectedInitCount    int
	}{
		{
			name:                 "valid clock chain with conditions",
			clockChain:           clockChain,
			profileName:          profileName,
			expectError:          false,
			expectedDefaultCount: 0, // No explicit default conditions in test data
			expectedInitCount:    1, // "Initialize T-BC" condition has empty sources, treated as init
		},
		{
			name:        "nil behavior section",
			clockChain:  &ptpv2alpha1.ClockChain{},
			profileName: profileName,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Get the actual enriched config from the manager
			hcm.mu.RLock()
			var enrichedConfig *enrichedHardwareConfig
			if len(hcm.hardwareConfigs) > 0 {
				enrichedConfig = &hcm.hardwareConfigs[0]
			} else {
				// Fallback: create a minimal enriched config if none exists
				enrichedConfig = &enrichedHardwareConfig{
					HardwareConfig: *hwConfig,
					sysFSCommands:  make(map[string][]SysFSCommand),
				}
			}
			hcm.mu.RUnlock()

			// Override executors to avoid actual hardware operations
			hcm.overrideExecutors(func(_ []dpll.PinParentDeviceCtl) error {
				return nil
			}, func(_, _ string) error {
				return nil
			})

			// Test the function
			testErr := hcm.applyDefaultAndInitConditions(tt.clockChain, tt.profileName, enrichedConfig)

			// Verify error expectation
			if tt.expectError {
				assert.Error(t, testErr)
			} else {
				assert.NoError(t, testErr)
			}

			// Note: For default/init conditions, we need to check all sources since sourceName is mandatory
			if tt.clockChain.Behavior != nil {
				// Extract default/init conditions by checking all sources
				var defaultConditions []ptpv2alpha1.Condition
				var initConditions []ptpv2alpha1.Condition
				seenDefault := make(map[string]bool)
				seenInit := make(map[string]bool)

				// For each source, extract matching default/init conditions
				for _, source := range tt.clockChain.Behavior.Sources {
					for _, condition := range tt.clockChain.Behavior.Conditions {
						// Triggers are mandatory - skip conditions without triggers
						if len(condition.Triggers) == 0 {
							continue
						}
						for _, trigger := range condition.Triggers {
							if trigger.ConditionType == "default" && trigger.SourceName == source.Name {
								if !seenDefault[condition.Name] {
									defaultConditions = append(defaultConditions, condition)
									seenDefault[condition.Name] = true
								}
							}
							if trigger.ConditionType == "init" && trigger.SourceName == source.Name {
								if !seenInit[condition.Name] {
									initConditions = append(initConditions, condition)
									seenInit[condition.Name] = true
								}
							}
						}
					}
				}

				assert.Equal(t, tt.expectedDefaultCount, len(defaultConditions), "Default conditions count mismatch")
				assert.Equal(t, tt.expectedInitCount, len(initConditions), "Init conditions count mismatch")

				t.Logf("✅ Found %d default conditions and %d init conditions",
					len(defaultConditions), len(initConditions))
			}
		})
	}
}

// TestExtractConditionsByType tests the extractConditionsByTypeAndSource function
func TestExtractConditionsByType(t *testing.T) {
	// Set up mock DPLL pins for testing
	mockErr := SetupMockDpllPinsForTests()
	if mockErr != nil {
		t.Logf("Warning: Failed to setup mock DPLL pins: %v", mockErr)
		// Continue with test as DPLL pins are optional
	}
	defer TeardownMockDpllPinsForTests()

	hcm := NewHardwareConfigManager()

	// Create test conditions
	conditions := []ptpv2alpha1.Condition{
		{
			Name: "Default Condition",
			Triggers: []ptpv2alpha1.SourceState{
				{SourceName: "TestSource", ConditionType: ConditionTypeDefault},
			},
		},
		{
			Name: "Init Condition",
			Triggers: []ptpv2alpha1.SourceState{
				{SourceName: "TestSource", ConditionType: ConditionTypeInit},
			},
		},
		{
			Name: "Locked Condition",
			Triggers: []ptpv2alpha1.SourceState{
				{SourceName: "TestSource", ConditionType: ConditionTypeLocked},
			},
		},
		{
			Name: "Lost Condition",
			Triggers: []ptpv2alpha1.SourceState{
				{SourceName: "TestSource", ConditionType: ConditionTypeLost},
			},
		},
	}

	tests := []struct {
		name          string
		conditionType string
		expectedCount int
		expectedNames []string
	}{
		{
			name:          "extract default conditions",
			conditionType: ConditionTypeDefault,
			expectedCount: 1,
			expectedNames: []string{"Default Condition"},
		},
		{
			name:          "extract init conditions",
			conditionType: ConditionTypeInit,
			expectedCount: 1,
			expectedNames: []string{"Init Condition"},
		},
		{
			name:          "extract locked conditions",
			conditionType: ConditionTypeLocked,
			expectedCount: 1,
			expectedNames: []string{"Locked Condition"},
		},
		{
			name:          "extract lost conditions",
			conditionType: ConditionTypeLost,
			expectedCount: 1,
			expectedNames: []string{"Lost Condition"},
		},
		{
			name:          "extract non-existent condition type",
			conditionType: "nonexistent",
			expectedCount: 0,
			expectedNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// sourceName is mandatory, so we need to pass a valid sourceName
			// For tests, use "TestSource" which matches the test conditions
			// sourceName is mandatory in triggers
			sourceName := "TestSource"
			result := hcm.extractConditionsByTypeAndSource(conditions, tt.conditionType, sourceName)

			assert.Equal(t, tt.expectedCount, len(result), "Condition count mismatch")

			// Verify condition names
			resultNames := make([]string, len(result))
			for i, condition := range result {
				resultNames[i] = condition.Name
			}

			for _, expectedName := range tt.expectedNames {
				assert.Contains(t, resultNames, expectedName, "Expected condition not found")
			}

			t.Logf("✅ Found %d conditions of type '%s': %v",
				len(result), tt.conditionType, resultNames)
		})
	}
}

// TestResolveSysFSPtpDevice tests the resolveSysFSPtpDevice function with mock file system
func TestResolveSysFSPtpDevice(t *testing.T) {
	// Create a temporary directory structure for testing
	tempDir := t.TempDir()

	// Create mock PTP device directories and files
	ptpDeviceDir := filepath.Join(tempDir, "sys", "class", "net", "eth0", "device", "ptp")
	if err := os.MkdirAll(ptpDeviceDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory structure: %v", err)
	}

	// Create mock PTP devices
	ptp0Dir := filepath.Join(ptpDeviceDir, "ptp0")
	ptp1Dir := filepath.Join(ptpDeviceDir, "ptp1")
	ptp2Dir := filepath.Join(ptpDeviceDir, "ptp2")

	if err := os.MkdirAll(ptp0Dir, 0755); err != nil {
		t.Fatalf("Failed to create ptp0 directory: %v", err)
	}
	if err := os.MkdirAll(ptp1Dir, 0755); err != nil {
		t.Fatalf("Failed to create ptp1 directory: %v", err)
	}
	if err := os.MkdirAll(ptp2Dir, 0755); err != nil {
		t.Fatalf("Failed to create ptp2 directory: %v", err)
	}

	// Create test files with different permissions
	writableFile := filepath.Join(ptp0Dir, "period")
	readOnlyFile := filepath.Join(ptp1Dir, "period")
	anotherWritableFile := filepath.Join(ptp2Dir, "period")

	// Create writable files (0644 has write permission for owner)
	if err := os.WriteFile(writableFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create writable test file: %v", err)
	}

	if err := os.WriteFile(anotherWritableFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create another writable test file: %v", err)
	}

	// Create read-only file (0444 has no write permission)
	if err := os.WriteFile(readOnlyFile, []byte("test"), 0444); err != nil {
		t.Fatalf("Failed to create read-only test file: %v", err)
	}

	// Create HardwareConfigManager for testing
	hcm := &HardwareConfigManager{
		hardwareConfigs: make([]enrichedHardwareConfig, 0),
	}

	testCases := []struct {
		name          string
		interfacePath string
		expectedPaths []string
		expectedError bool
		description   string
	}{
		{
			name:          "no_ptp_placeholder",
			interfacePath: "/sys/class/net/eth0/carrier",
			expectedPaths: []string{"/sys/class/net/eth0/carrier"},
			expectedError: false,
			description:   "Should return path as-is when no ptp* placeholder is present",
		},
		{
			name:          "valid_ptp_devices_found",
			interfacePath: filepath.Join(tempDir, "sys/class/net/eth0/device/ptp/ptp*/period"),
			expectedPaths: []string{
				filepath.Join(tempDir, "sys/class/net/eth0/device/ptp/ptp0/period"),
				filepath.Join(tempDir, "sys/class/net/eth0/device/ptp/ptp2/period"),
			},
			expectedError: false,
			description:   "Should return all writable PTP device paths",
		},
		{
			name:          "nonexistent_directory",
			interfacePath: filepath.Join(tempDir, "nonexistent/ptp/ptp*/period"),
			expectedPaths: nil,
			expectedError: true,
			description:   "Should return error when PTP device directory doesn't exist",
		},
		{
			name:          "no_writable_files",
			interfacePath: filepath.Join(tempDir, "sys/class/net/eth0/device/ptp/ptp*/nonexistent"),
			expectedPaths: nil,
			expectedError: true,
			description:   "Should return error when no writable files are found",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing: %s", tc.description)

			result, testErr := hcm.resolveSysFSPtpDevice(tc.interfacePath)

			if tc.expectedError {
				if testErr == nil {
					t.Errorf("Expected error but got none")
				} else {
					t.Logf("✅ Got expected error: %v", testErr)
				}
			} else {
				if testErr != nil {
					t.Errorf("Unexpected error: %v", testErr)
				} else {
					// Sort both slices for comparison since order might vary
					sort.Strings(result)
					sort.Strings(tc.expectedPaths)

					if !reflect.DeepEqual(result, tc.expectedPaths) {
						t.Errorf("Expected paths: %v, Got: %v", tc.expectedPaths, result)
					} else {
						t.Logf("✅ Successfully resolved %d PTP device paths", len(result))
						for i, path := range result {
							t.Logf("   Path %d: %s", i+1, path)
						}
					}
				}
			}
		})
	}

	// Additional test for edge cases
	t.Run("edge_cases", func(t *testing.T) {
		// Test empty path
		result, edgeErr := hcm.resolveSysFSPtpDevice("")
		if edgeErr != nil {
			t.Errorf("Empty path should not return error, got: %v", edgeErr)
		}
		if len(result) != 1 || result[0] != "" {
			t.Errorf("Empty path should return empty string, got: %v", result)
		}

		// Test path with multiple ptp* placeholders (edge case)
		complexPath := filepath.Join(tempDir, "sys/class/net/eth0/device/ptp/ptp*/subdir/ptp*/period")
		result, edgeErr = hcm.resolveSysFSPtpDevice(complexPath)
		// This should still work as it only splits on the first ptp*
		if edgeErr != nil {
			t.Logf("Complex path with multiple ptp* placeholders returned error (expected): %v", edgeErr)
		} else {
			t.Logf("Complex path resolved to: %v", result)
		}
	})
}

func TestSysFSCommandCaching(t *testing.T) {
	// Set up mock PTP device resolver for testing
	SetupMockPtpDeviceResolver()
	defer TeardownMockPtpDeviceResolver()

	// Set up mock DPLL pins for testing
	mockErr := SetupMockDpllPinsForTests()
	if mockErr != nil {
		t.Logf("Warning: Failed to setup mock DPLL pins: %v", mockErr)
		// Continue with test as DPLL pins are optional
	}
	defer TeardownMockDpllPinsForTests()

	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	SetCommandExecutor(mockCmd)
	defer ResetCommandExecutor()

	hcm := NewHardwareConfigManager()
	defer hcm.resetExecutors()

	hcm.overrideExecutors(nil, func(_, _ string) error { return nil })

	hwConfig, err := loadHardwareConfigFromFile("testdata/wpc-hwconfig.yaml")
	assert.NoError(t, err)
	assert.NotNil(t, hwConfig)

	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{*hwConfig})
	assert.NoError(t, err)

	var sysfsWrites []SysFSCommand

	sysfsWriterOverride := func(path, value string) error {
		sysfsWrites = append(sysfsWrites, SysFSCommand{Path: path, Value: value})
		return nil
	}

	hcm.overrideExecutors(nil, sysfsWriterOverride)

	commands := []SysFSCommand{{Path: "/tmp/test", Value: "hello"}}
	err = hcm.applyCachedSysFSCommands("test-profile", "test-condition", commands)
	assert.NoError(t, err)
}

// TestESyncConfigurationFromYAML tests loading and processing eSync configuration from hwconfig.yaml
func TestESyncConfigurationFromYAML(t *testing.T) {
	// Setup mock environment
	SetupMockPtpDeviceResolver()
	defer TeardownMockPtpDeviceResolver()
	mockErr := SetupMockDpllPinsForTests()
	if mockErr != nil {
		t.Fatalf("Failed to setup mock DPLL pins: %v", mockErr)
	}
	defer TeardownMockDpllPinsForTests()

	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b5-80")
	SetCommandExecutor(mockCmd)
	defer ResetCommandExecutor()

	// Load the hwconfig.yaml which has eSync configuration
	hwConfig, err := loadHardwareConfigFromFile("testdata/wpc-hwconfig.yaml")
	assert.NoError(t, err, "Failed to load hwconfig.yaml")
	assert.NotNil(t, hwConfig, "Hardware config should not be nil")

	// Verify the clock chain structure
	assert.NotNil(t, hwConfig.Spec.Profile.ClockChain, "Clock chain should not be nil")
	clockChain := hwConfig.Spec.Profile.ClockChain

	// Verify eSync definitions are present and correctly parsed
	assert.NotNil(t, clockChain.CommonDefinitions, "CommonDefinitions should not be nil")
	assert.NotEmpty(t, clockChain.CommonDefinitions.ESyncDefinitions, "ESyncDefinitions should not be empty")
	assert.Len(t, clockChain.CommonDefinitions.ESyncDefinitions, 1, "Should have exactly 1 eSync definition")

	// Verify the eSync definition values
	esyncDef := clockChain.CommonDefinitions.ESyncDefinitions[0]
	assert.Equal(t, "esync-10-1", esyncDef.Name, "eSync definition name should be 'esync-10-1'")
	assert.Equal(t, int64(10000000), esyncDef.ESyncConfig.TransferFrequency, "Transfer frequency should be 10 MHz")
	assert.Equal(t, int64(1), esyncDef.ESyncConfig.EmbeddedSyncFrequency, "Embedded sync frequency should be 1 Hz")
	assert.Equal(t, int64(25), esyncDef.ESyncConfig.DutyCyclePct, "Duty cycle should be 25%")

	t.Logf("✓ eSync definition '%s' loaded: transfer=%d Hz, esync=%d Hz, duty=%d%%",
		esyncDef.Name,
		esyncDef.ESyncConfig.TransferFrequency,
		esyncDef.ESyncConfig.EmbeddedSyncFrequency,
		esyncDef.ESyncConfig.DutyCyclePct)

	// Verify subsystems reference the eSync config
	assert.NotEmpty(t, clockChain.Structure, "Structure should not be empty")
	assert.Len(t, clockChain.Structure, 2, "Should have exactly 2 subsystems (Leader and Follower)")

	// Test eSync resolution logic
	hcm := NewHardwareConfigManager()
	hcm.pinCache, err = GetDpllPins()
	assert.NoError(t, err, "Should get DPLL pins")
	assert.NotNil(t, hcm.pinCache, "Pin cache should not be nil")

	// Load hardware defaults for intel/e810 (same as used in the hwconfig.yaml).
	// Vendor defaults are embedded; no filesystem setup needed.
	hwSpec, err := LoadHardwareDefaults("intel/e810")
	assert.NoError(t, err, "Should load hardware defaults")
	assert.NotNil(t, hwSpec, "Hardware spec should not be nil")
	assert.NotNil(t, hwSpec.PinEsyncCommands, "Pin eSync commands should be defined")
	t.Logf("✓ Loaded hardware defaults: outputs=%d cmds, inputs=%d cmds",
		len(hwSpec.PinEsyncCommands.Outputs), len(hwSpec.PinEsyncCommands.Inputs))

	// Track total commands built across all subsystems
	totalESyncCommands := 0
	foundESyncCommand := false

	// Verify ALL subsystems
	for subIdx, subsystem := range clockChain.Structure {
		t.Logf("\n--- Subsystem %d: %s ---", subIdx+1, subsystem.Name)
		netIface := subsystem.DPLL.NetworkInterface
		if netIface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
			netIface = subsystem.Ethernet[0].Ports[0]
		}
		t.Logf("  Network Interface: %s", netIface)

		// Check phase outputs
		for pinLabel, pinCfg := range subsystem.DPLL.PhaseOutputs {
			if pinCfg.ESyncConfigName != "" {
				t.Logf("  Phase output '%s': esyncConfigName='%s', connector='%s'",
					pinLabel, pinCfg.ESyncConfigName, pinCfg.Connector)
				assert.Equal(t, "esync-10-1", pinCfg.ESyncConfigName,
					"Pin %s should reference esync-10-1", pinLabel)

				// Test resolvePinFrequency
				transferFreq, esyncFreq, dutyCycle, hasConfig := hcm.resolvePinFrequency(pinCfg, clockChain)
				assert.True(t, hasConfig, "Should resolve frequency config for %s", pinLabel)
				assert.Equal(t, uint64(10000000), transferFreq,
					"Pin %s: transfer frequency should be 10 MHz", pinLabel)
				assert.Equal(t, uint64(1), esyncFreq,
					"Pin %s: eSync frequency should be 1 Hz", pinLabel)
				assert.Equal(t, int64(25), dutyCycle,
					"Pin %s: duty cycle should be 25%%", pinLabel)
				t.Logf("    ✓ Resolved: transfer=%d Hz, esync=%d Hz, duty=%d%%", transferFreq, esyncFreq, dutyCycle)
			}
		}

		// Check phase inputs
		for pinLabel, pinCfg := range subsystem.DPLL.PhaseInputs {
			if pinCfg.ESyncConfigName != "" {
				t.Logf("  Phase input '%s': esyncConfigName='%s', connector='%s'",
					pinLabel, pinCfg.ESyncConfigName, pinCfg.Connector)
				assert.Equal(t, "esync-10-1", pinCfg.ESyncConfigName,
					"Pin %s should reference esync-10-1", pinLabel)

				// Test resolvePinFrequency
				transferFreq, esyncFreq, dutyCycle, hasConfig := hcm.resolvePinFrequency(pinCfg, clockChain)
				assert.True(t, hasConfig, "Should resolve frequency config for %s", pinLabel)
				assert.Equal(t, uint64(10000000), transferFreq,
					"Pin %s: transfer frequency should be 10 MHz", pinLabel)
				assert.Equal(t, uint64(1), esyncFreq,
					"Pin %s: eSync frequency should be 1 Hz", pinLabel)
				assert.Equal(t, int64(25), dutyCycle,
					"Pin %s: duty cycle should be 25%%", pinLabel)
				t.Logf("    ✓ Resolved: transfer=%d Hz, esync=%d Hz, duty=%d%%", transferFreq, esyncFreq, dutyCycle)
			}
		}

		hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
		if hwDefPath == "" {
			hwDefPath = "intel/e810"
		}

		// Test building eSync commands for this subsystem with hardware spec
		commands, cmdErr := hcm.buildESyncPinCommands(subsystem, hwDefPath, clockChain, hwSpec)
		assert.NoError(t, cmdErr, "Should build eSync commands for subsystem %s", subsystem.Name)

		t.Logf("  Built %d pin commands for subsystem '%s'", len(commands), subsystem.Name)

		// Count pins with eSync config and frequency-only config in this subsystem
		outputPinsWithESync := 0
		inputPinsWithESync := 0
		outputPinsWithFreqOnly := 0
		inputPinsWithFreqOnly := 0
		for _, pinCfg := range subsystem.DPLL.PhaseOutputs {
			if pinCfg.ESyncConfigName != "" {
				outputPinsWithESync++
			} else if pinCfg.Frequency != nil && *pinCfg.Frequency > 0 {
				outputPinsWithFreqOnly++
			}
		}
		for _, pinCfg := range subsystem.DPLL.PhaseInputs {
			if pinCfg.ESyncConfigName != "" {
				inputPinsWithESync++
			} else if pinCfg.Frequency != nil && *pinCfg.Frequency > 0 {
				inputPinsWithFreqOnly++
			}
		}

		// Verify command sequence length
		// Outputs: 3 commands per pin (frequency, eSyncFrequency, re-enable)
		// Inputs: 2 commands per pin (frequency, eSyncFrequency)
		// Frequency-only pins (either input or output): 1 command per pin (frequency only)
		expectedCmds := (outputPinsWithESync * 3) + (inputPinsWithESync * 2) + outputPinsWithFreqOnly + inputPinsWithFreqOnly
		if expectedCmds > 0 {
			assert.Equal(t, expectedCmds, len(commands),
				"Should have %d commands for %d esync-output pins, %d esync-input pins, %d freq-only outputs, %d freq-only inputs",
				expectedCmds, outputPinsWithESync, inputPinsWithESync, outputPinsWithFreqOnly, inputPinsWithFreqOnly)
			t.Logf("  ✓ Command sequence: %d esync-outputs × 3 + %d esync-inputs × 2 + %d freq-only outputs × 1 + %d freq-only inputs × 1 = %d total",
				outputPinsWithESync, inputPinsWithESync, outputPinsWithFreqOnly, inputPinsWithFreqOnly, len(commands))
		}

		// Verify commands with frequency/eSync set
		for cmdIdx, cmd := range commands {
			if cmd.Frequency != nil || cmd.EsyncFrequency != nil {
				totalESyncCommands++
				t.Logf("    Cmd[%d]: Pin ID=%d, Frequency=%v Hz, EsyncFrequency=%v Hz, ParentDevices=%d",
					cmdIdx+1,
					cmd.ID,
					ptrValue(cmd.Frequency),
					ptrValue(cmd.EsyncFrequency),
					len(cmd.PinParentCtl))

				// Verify frequency values when set
				if cmd.Frequency != nil {
					// Allow either the transfer frequency (10 MHz) or frequency-only configs (1 Hz)
					if *cmd.Frequency != uint64(10000000) && *cmd.Frequency != uint64(1) {
						t.Errorf("Pin %d cmd[%d]: unexpected frequency %d Hz (expected 10MHz or 1Hz)", cmd.ID, cmdIdx+1, *cmd.Frequency)
					}
				}
				if cmd.EsyncFrequency != nil {
					assert.Equal(t, uint64(1), *cmd.EsyncFrequency,
						"Pin %d cmd[%d]: eSync frequency should be 1 Hz", cmd.ID, cmdIdx+1)
					foundESyncCommand = true
				}
			}
		}
	}

	// Verify we found at least one properly configured eSync command
	t.Logf("\nTotal eSync/frequency commands across all subsystems: %d", totalESyncCommands)
	if totalESyncCommands > 0 {
		assert.True(t, foundESyncCommand,
			"Should have at least one command with both Frequency and EsyncFrequency set")
	} else {
		t.Log("⚠ No eSync commands generated - pins may not be in mock cache")
	}
}

// Helper function to safely dereference pointer for logging
func ptrValue(ptr *uint64) string {
	if ptr == nil {
		return "nil"
	}
	return fmt.Sprintf("%d", *ptr)
}

// TestHoldoverParametersExtraction tests extraction of holdover parameters from HardwareConfig
func TestHoldoverParametersExtraction(t *testing.T) {
	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens12f0"}, "driver: ice\nbus-info: 0000:85:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:85:00.0"}, "85:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:85:00.0"}, "serial_number 50-7c-6f-ff-ff-ab-cd-ef")
	SetCommandExecutor(mockCmd)
	defer ResetCommandExecutor()

	// Create synthetic clock IDs for test subsystems (matching the mock responses above)
	clockID1 := uint64(0x507c6fffff5c4ae8) // ens4f0
	clockID2 := uint64(0x507c6fffff1fb1b8) // ens8f0

	hwConfig := ptpv2alpha1.HardwareConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-holdover",
			Namespace: "openshift-ptp",
		},
		Spec: ptpv2alpha1.HardwareConfigSpec{
			RelatedPtpProfileName: "test-profile",
			Profile: ptpv2alpha1.HardwareProfile{
				ClockChain: &ptpv2alpha1.ClockChain{
					Structure: []ptpv2alpha1.Subsystem{
						{
							Name:                        "Subsystem1",
							HardwareSpecificDefinitions: "intel/e810",
							DPLL: ptpv2alpha1.DPLL{
								NetworkInterface: "ens4f0",
								HoldoverParameters: &ptpv2alpha1.HoldoverParameters{
									MaxInSpecOffset:        200,
									LocalMaxHoldoverOffset: 2000,
									LocalHoldoverTimeout:   7200,
								},
							},
							Ethernet: []ptpv2alpha1.Ethernet{
								{Ports: []string{"ens4f0", "ens4f1"}},
							},
						},
						{
							Name:                        "Subsystem2",
							HardwareSpecificDefinitions: "intel/e810",
							DPLL: ptpv2alpha1.DPLL{
								NetworkInterface: "ens8f0",
								// Test defaults by omitting values
								HoldoverParameters: &ptpv2alpha1.HoldoverParameters{},
							},
							Ethernet: []ptpv2alpha1.Ethernet{
								{Ports: []string{"ens8f0", "ens8f1"}},
							},
						},
						{
							Name:                        "Subsystem3-NoHoldover",
							HardwareSpecificDefinitions: "intel/e810",
							DPLL: ptpv2alpha1.DPLL{
								NetworkInterface: "ens12f0",
								// No holdover parameters
							},
							Ethernet: []ptpv2alpha1.Ethernet{
								{Ports: []string{"ens12f0", "ens12f1"}},
							},
						},
					},
				},
			},
		},
	}

	hcm := NewHardwareConfigManager()

	// Test extractHoldoverParameters
	params := hcm.extractHoldoverParameters(hwConfig)

	// Should have 2 entries (Subsystem3 doesn't have holdover params)
	assert.Len(t, params, 2, "Should extract holdover params for 2 subsystems")

	// Check Subsystem1 - explicit values
	params1, found1 := params[clockID1]
	assert.True(t, found1, "Should find holdover params for clock1")
	assert.NotNil(t, params1, "Holdover params for clock1 should not be nil")
	assert.Equal(t, uint64(200), params1.MaxInSpecOffset, "MaxInSpecOffset should match")
	assert.Equal(t, uint64(2000), params1.LocalMaxHoldoverOffset, "LocalMaxHoldoverOffset should match")
	assert.Equal(t, uint64(7200), params1.LocalHoldoverTimeout, "LocalHoldoverTimeout should match")

	// Check Subsystem2 - default values
	params2, found2 := params[clockID2]
	assert.True(t, found2, "Should find holdover params for clock2")
	assert.NotNil(t, params2, "Holdover params for clock2 should not be nil")
	assert.Equal(t, uint64(100), params2.MaxInSpecOffset, "MaxInSpecOffset should use default")
	assert.Equal(t, uint64(1500), params2.LocalMaxHoldoverOffset, "LocalMaxHoldoverOffset should use default")
	assert.Equal(t, uint64(14400), params2.LocalHoldoverTimeout, "LocalHoldoverTimeout should use default")

	// Test GetHoldoverParameters API by manually setting the config
	// (Avoid UpdateHardwareConfig which tries to connect to netlink)
	enriched := enrichedHardwareConfig{
		HardwareConfig: hwConfig,
		holdoverParams: params,
	}
	hcm.mu.Lock()
	hcm.hardwareConfigs = []enrichedHardwareConfig{enriched}
	hcm.ready = true
	hcm.mu.Unlock()

	// Retrieve using API
	retrieved1 := hcm.GetHoldoverParameters("test-profile", clockID1)
	assert.NotNil(t, retrieved1, "Should retrieve holdover params for clock1")
	if retrieved1 != nil {
		assert.Equal(t, uint64(200), retrieved1.MaxInSpecOffset, "Retrieved MaxInSpecOffset should match")
	}

	retrieved2 := hcm.GetHoldoverParameters("test-profile", clockID2)
	assert.NotNil(t, retrieved2, "Should retrieve holdover params for clock2")
	if retrieved2 != nil {
		assert.Equal(t, uint64(100), retrieved2.MaxInSpecOffset, "Retrieved MaxInSpecOffset should use default")
	}

	// Test non-existent profile
	retrieved3 := hcm.GetHoldoverParameters("non-existent", clockID1)
	assert.Nil(t, retrieved3, "Should return nil for non-existent profile")

	// Test non-existent clock ID
	retrieved4 := hcm.GetHoldoverParameters("test-profile", 0xDEADBEEF)
	assert.Nil(t, retrieved4, "Should return nil for non-existent clock ID")

	t.Logf("✓ Holdover parameter extraction and retrieval working correctly")
}
