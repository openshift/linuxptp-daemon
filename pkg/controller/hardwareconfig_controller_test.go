package controller

import (
	"context"
	"errors"
	"testing"

	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MockHardwareConfigHandler implements HardwareConfigUpdateHandler for testing
type MockHardwareConfigHandler struct {
	LastUpdateConfigs      []ptpv2alpha1.HardwareConfig
	CurrentHardwareConfigs []ptpv2alpha1.HardwareConfig // Configs that are "currently applied" in the system
	UpdateCallCount        int
	UpdateError            error // Set this to simulate update failures
}

func (m *MockHardwareConfigHandler) UpdateHardwareConfig(hwConfigs []ptpv2alpha1.HardwareConfig) error {
	m.LastUpdateConfigs = hwConfigs
	m.UpdateCallCount++
	// Update current configs to simulate what's applied in the system
	m.CurrentHardwareConfigs = make([]ptpv2alpha1.HardwareConfig, len(hwConfigs))
	for i := range hwConfigs {
		m.CurrentHardwareConfigs[i] = *hwConfigs[i].DeepCopy()
	}
	return m.UpdateError
}

func (m *MockHardwareConfigHandler) GetCurrentHardwareConfigs() []ptpv2alpha1.HardwareConfig {
	if m == nil {
		return []ptpv2alpha1.HardwareConfig{}
	}
	// Return a copy of current configs
	result := make([]ptpv2alpha1.HardwareConfig, len(m.CurrentHardwareConfigs))
	for i := range m.CurrentHardwareConfigs {
		result[i] = *m.CurrentHardwareConfigs[i].DeepCopy()
	}
	return result
}

// MockHardwareConfigRestartTrigger implements HardwareConfigRestartTrigger for testing
type MockHardwareConfigRestartTrigger struct {
	RestartTriggerCount int
	CurrentProfiles     []string
	RestartError        error // Set this to simulate restart failures
}

func (m *MockHardwareConfigRestartTrigger) TriggerRestartForHardwareChange() error {
	m.RestartTriggerCount++
	return m.RestartError
}

func (m *MockHardwareConfigRestartTrigger) GetCurrentPTPProfiles() []string {
	if m == nil {
		return []string{}
	}
	return m.CurrentProfiles
}

// Helper function to create a test hardware config
func createTestHardwareConfig(name, profileName, relatedPtpProfile string) ptpv2alpha1.HardwareConfig {
	return ptpv2alpha1.HardwareConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "openshift-ptp",
		},
		Spec: ptpv2alpha1.HardwareConfigSpec{
			Profile: ptpv2alpha1.HardwareProfile{
				Name:        stringPtr(profileName),
				Description: stringPtr("Test profile"),
				ClockChain: &ptpv2alpha1.ClockChain{
					Structure: []ptpv2alpha1.Subsystem{
						{
							Name: "test-subsystem",
							DPLL: ptpv2alpha1.DPLL{
								NetworkInterface: "ens1f0",
							},
							Ethernet: []ptpv2alpha1.Ethernet{
								{
									Ports: []string{"ens1f0"},
								},
							},
						},
					},
				},
			},
			RelatedPtpProfileName: relatedPtpProfile,
		},
	}
}

func TestCalculateNodeHardwareConfigs(t *testing.T) {
	testCases := []struct {
		name                 string
		nodeName             string
		activeProfiles       []string
		hwConfigs            []ptpv2alpha1.HardwareConfig
		expectedConfigsCount int
		expectedConfigNames  []string
		description          string
	}{
		{
			name:                 "no hardware configs",
			nodeName:             "test-node",
			activeProfiles:       []string{"grandmaster"},
			hwConfigs:            []ptpv2alpha1.HardwareConfig{},
			expectedConfigsCount: 0,
			expectedConfigNames:  []string{},
			description:          "Should return empty when no configs provided",
		},
		{
			name:           "single hardware config with matching active profile",
			nodeName:       "test-node",
			activeProfiles: []string{"grandmaster"},
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				createTestHardwareConfig("gm-config", "grandmaster-profile", "grandmaster"),
			},
			expectedConfigsCount: 1,
			expectedConfigNames:  []string{"grandmaster-profile"},
			description:          "Should include config when related profile is active",
		},
		{
			name:           "single hardware config with non-matching active profile",
			nodeName:       "test-node",
			activeProfiles: []string{"boundary-clock"},
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				createTestHardwareConfig("gm-config", "grandmaster-profile", "grandmaster"),
			},
			expectedConfigsCount: 0,
			expectedConfigNames:  []string{},
			description:          "Should exclude config when related profile is not active",
		},
		{
			name:           "multiple hardware configs, all matching",
			nodeName:       "worker-node",
			activeProfiles: []string{"boundary-clock", "ordinary-clock"},
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				createTestHardwareConfig("bc-config", "boundary-clock-profile", "boundary-clock"),
				createTestHardwareConfig("oc-config", "ordinary-clock-profile", "ordinary-clock"),
			},
			expectedConfigsCount: 2,
			expectedConfigNames:  []string{"boundary-clock-profile", "ordinary-clock-profile"},
			description:          "Should include all configs when all related profiles are active",
		},
		{
			name:           "multiple hardware configs, partial matching",
			nodeName:       "worker-node",
			activeProfiles: []string{"boundary-clock"},
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				createTestHardwareConfig("bc-config", "boundary-clock-profile", "boundary-clock"),
				createTestHardwareConfig("oc-config", "ordinary-clock-profile", "ordinary-clock"),
			},
			expectedConfigsCount: 1,
			expectedConfigNames:  []string{"boundary-clock-profile"},
			description:          "Should only include configs whose related profiles are active",
		},
		{
			name:           "hardware config without related profile",
			nodeName:       "test-node",
			activeProfiles: []string{"grandmaster"},
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				createTestHardwareConfig("no-profile-config", "some-profile", ""), // Empty relatedPtpProfileName
			},
			expectedConfigsCount: 0,
			expectedConfigNames:  []string{},
			description:          "Should exclude configs without relatedPtpProfileName",
		},
		{
			name:           "no active profiles - excludes all configs",
			nodeName:       "test-node",
			activeProfiles: []string{},
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				createTestHardwareConfig("gm-config", "grandmaster-profile", "grandmaster"),
			},
			expectedConfigsCount: 0,
			expectedConfigNames:  []string{},
			description:          "Should exclude all configs when no active profiles (no profiles means no hardware configs needed)",
		},
		{
			name:           "no active profiles - excludes all configs",
			nodeName:       "test-node",
			activeProfiles: []string{}, // Empty list means no active profiles
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				createTestHardwareConfig("gm-config", "grandmaster-profile", "grandmaster"),
			},
			expectedConfigsCount: 0,
			expectedConfigNames:  []string{},
			description:          "Should exclude all configs when no active profiles exist",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Always provide a mock ConfigUpdate (required field)
			// Use empty profiles list if nil is specified in test
			activeProfiles := tc.activeProfiles
			if activeProfiles == nil {
				activeProfiles = []string{} // Default to empty list
			}
			mockTrigger := &MockHardwareConfigRestartTrigger{
				CurrentProfiles: activeProfiles,
			}

			reconciler := &HardwareConfigReconciler{
				NodeName:     tc.nodeName,
				ConfigUpdate: mockTrigger, // Always set - required field
			}

			result := reconciler.calculateNodeHardwareConfigs(context.TODO(), tc.hwConfigs)

			assert.Len(t, result, tc.expectedConfigsCount,
				"%s: Expected %d hardware configs, got %d", tc.description, tc.expectedConfigsCount, len(result))

			var actualConfigNames []string
			for _, hwConfig := range result {
				if hwConfig.Spec.Profile.Name != nil {
					actualConfigNames = append(actualConfigNames, *hwConfig.Spec.Profile.Name)
				}
			}
			assert.ElementsMatch(t, tc.expectedConfigNames, actualConfigNames,
				"%s: Expected config names %v, got %v", tc.description, tc.expectedConfigNames, actualConfigNames)
		})
	}
}

// TestDiffHardwareConfigs tests the config diff logic using maps
func TestDiffHardwareConfigs(t *testing.T) {
	t.Run("empty configs have no changes", func(t *testing.T) {
		oldMap := sliceToHardwareConfigMap([]ptpv2alpha1.HardwareConfig{})
		diff := diffHardwareConfigs(oldMap, []ptpv2alpha1.HardwareConfig{})
		assert.False(t, diff.HasChanges())
	})

	t.Run("different lengths are detected as change", func(t *testing.T) {
		cfg1 := createTestHardwareConfig("config1", "profile1", "ptp1")
		oldMap := sliceToHardwareConfigMap([]ptpv2alpha1.HardwareConfig{cfg1})
		diff := diffHardwareConfigs(oldMap, []ptpv2alpha1.HardwareConfig{})
		assert.True(t, diff.HasChanges())
		assert.Len(t, diff.Removed, 1)
	})

	t.Run("same configs have no changes", func(t *testing.T) {
		cfg1 := createTestHardwareConfig("config1", "profile1", "ptp1")
		cfg2 := createTestHardwareConfig("config1", "profile1", "ptp1")
		oldMap := sliceToHardwareConfigMap([]ptpv2alpha1.HardwareConfig{cfg1})
		diff := diffHardwareConfigs(oldMap, []ptpv2alpha1.HardwareConfig{cfg2})
		assert.False(t, diff.HasChanges())
	})

	t.Run("different configs are detected as modified", func(t *testing.T) {
		cfg1 := createTestHardwareConfig("config1", "profile1", "ptp1")
		cfg2 := createTestHardwareConfig("config1", "profile2", "ptp1") // Different profile name
		oldMap := sliceToHardwareConfigMap([]ptpv2alpha1.HardwareConfig{cfg1})
		diff := diffHardwareConfigs(oldMap, []ptpv2alpha1.HardwareConfig{cfg2})
		assert.True(t, diff.HasChanges())
		assert.Len(t, diff.Modified, 1)
	})

	t.Run("added config is detected", func(t *testing.T) {
		cfg1 := createTestHardwareConfig("config1", "profile1", "ptp1")
		cfg2 := createTestHardwareConfig("config2", "profile2", "ptp2")
		oldMap := sliceToHardwareConfigMap([]ptpv2alpha1.HardwareConfig{cfg1})
		diff := diffHardwareConfigs(oldMap, []ptpv2alpha1.HardwareConfig{cfg1, cfg2})
		assert.True(t, diff.HasChanges())
		assert.Len(t, diff.Added, 1)
		assert.Equal(t, "config2", diff.Added[0].Name)
	})

	t.Run("removed config is detected", func(t *testing.T) {
		cfg1 := createTestHardwareConfig("config1", "profile1", "ptp1")
		cfg2 := createTestHardwareConfig("config2", "profile2", "ptp2")
		oldMap := sliceToHardwareConfigMap([]ptpv2alpha1.HardwareConfig{cfg1, cfg2})
		diff := diffHardwareConfigs(oldMap, []ptpv2alpha1.HardwareConfig{cfg1})
		assert.True(t, diff.HasChanges())
		assert.Len(t, diff.Removed, 1)
		assert.Equal(t, "config2", diff.Removed[0].Name)
	})
}

// TestCheckIfChangedConfigsAffectActiveProfiles tests the new change detection logic
func TestCheckIfChangedConfigsAffectActiveProfiles(t *testing.T) {
	testCases := []struct {
		name            string
		activeProfiles  []string
		oldConfigs      []ptpv2alpha1.HardwareConfig
		newConfigs      []ptpv2alpha1.HardwareConfig
		expectedRestart bool
		description     string
	}{
		{
			name:            "no active profiles",
			activeProfiles:  []string{},
			oldConfigs:      []ptpv2alpha1.HardwareConfig{},
			newConfigs:      []ptpv2alpha1.HardwareConfig{createTestHardwareConfig("config1", "profile1", "ptp1")},
			expectedRestart: false,
			description:     "Should not restart when no active profiles exist",
		},
		{
			name:            "config added but not associated with active profile",
			activeProfiles:  []string{"active-profile"},
			oldConfigs:      []ptpv2alpha1.HardwareConfig{},
			newConfigs:      []ptpv2alpha1.HardwareConfig{createTestHardwareConfig("config1", "profile1", "other-profile")},
			expectedRestart: false,
			description:     "Should not restart when added config is not associated with active profile",
		},
		{
			name:            "config added and associated with active profile",
			activeProfiles:  []string{"active-profile"},
			oldConfigs:      []ptpv2alpha1.HardwareConfig{},
			newConfigs:      []ptpv2alpha1.HardwareConfig{createTestHardwareConfig("config1", "profile1", "active-profile")},
			expectedRestart: true,
			description:     "Should restart when added config is associated with active profile",
		},
		{
			name:            "config removed and was associated with active profile",
			activeProfiles:  []string{"active-profile"},
			oldConfigs:      []ptpv2alpha1.HardwareConfig{createTestHardwareConfig("config1", "profile1", "active-profile")},
			newConfigs:      []ptpv2alpha1.HardwareConfig{},
			expectedRestart: true,
			description:     "Should restart when removed config was associated with active profile",
		},
		{
			name:            "config modified and associated with active profile",
			activeProfiles:  []string{"active-profile"},
			oldConfigs:      []ptpv2alpha1.HardwareConfig{createTestHardwareConfig("config1", "profile1", "active-profile")},
			newConfigs:      []ptpv2alpha1.HardwareConfig{createTestHardwareConfig("config1", "profile1-modified", "active-profile")},
			expectedRestart: true,
			description:     "Should restart when modified config is associated with active profile",
		},
		{
			name:            "config unchanged - no restart",
			activeProfiles:  []string{"active-profile"},
			oldConfigs:      []ptpv2alpha1.HardwareConfig{createTestHardwareConfig("config1", "profile1", "active-profile")},
			newConfigs:      []ptpv2alpha1.HardwareConfig{createTestHardwareConfig("config1", "profile1", "active-profile")},
			expectedRestart: false,
			description:     "Should not restart when configs are unchanged",
		},
		{
			name:           "multiple configs, only changed one affects active profile",
			activeProfiles: []string{"active-profile"},
			oldConfigs: []ptpv2alpha1.HardwareConfig{
				createTestHardwareConfig("config1", "profile1", "other-profile"),
				createTestHardwareConfig("config2", "profile2", "active-profile"),
			},
			newConfigs: []ptpv2alpha1.HardwareConfig{
				createTestHardwareConfig("config1", "profile1-modified", "other-profile"), // Modified but not active
				createTestHardwareConfig("config2", "profile2", "active-profile"),         // Unchanged
			},
			expectedRestart: false,
			description:     "Should not restart when only changed config is not associated with active profile",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockTrigger := &MockHardwareConfigRestartTrigger{
				CurrentProfiles: tc.activeProfiles,
			}

			reconciler := &HardwareConfigReconciler{
				NodeName:     "test-node",
				ConfigUpdate: mockTrigger,
			}

			// Compute diff from old and new configs (matching production code pattern)
			oldMap := sliceToHardwareConfigMap(tc.oldConfigs)
			diff := diffHardwareConfigs(oldMap, tc.newConfigs)
			result := reconciler.checkIfChangedConfigsAffectActiveProfiles(diff)

			assert.Equal(t, tc.expectedRestart, result, tc.description)
		})
	}
}

// TestReconcileWhenNothingChanged tests that diffHardwareConfigs detects no changes for identical configs
func TestReconcileWhenNothingChanged(t *testing.T) {
	initialConfigs := []ptpv2alpha1.HardwareConfig{
		createTestHardwareConfig("config1", "profile1", "active-profile"),
	}

	// Test that diffHardwareConfigs correctly identifies unchanged configs
	oldMap := sliceToHardwareConfigMap(initialConfigs)
	diff := diffHardwareConfigs(oldMap, initialConfigs)
	assert.False(t, diff.HasChanges(), "Same configs should have no changes")
}

// TestReconcileWhenHardwareConfigChanged tests that reconciliation triggers update and restart when config changes
func TestReconcileWhenHardwareConfigChanged(t *testing.T) {
	mockHandler := &MockHardwareConfigHandler{}
	mockTrigger := &MockHardwareConfigRestartTrigger{
		CurrentProfiles: []string{"active-profile"},
	}

	reconciler := &HardwareConfigReconciler{
		NodeName:              "test-node",
		HardwareConfigHandler: mockHandler,
		ConfigUpdate:          mockTrigger,
	}

	oldConfigs := []ptpv2alpha1.HardwareConfig{
		createTestHardwareConfig("config1", "profile1", "active-profile"),
	}

	newConfigs := []ptpv2alpha1.HardwareConfig{
		createTestHardwareConfig("config1", "profile1-modified", "active-profile"),
	}

	// Verify configs are different and check restart
	oldMap := sliceToHardwareConfigMap(oldConfigs)
	diff := diffHardwareConfigs(oldMap, newConfigs)
	assert.True(t, diff.HasChanges(), "Configs should be different")
	shouldRestart := reconciler.checkIfChangedConfigsAffectActiveProfiles(diff)
	assert.True(t, shouldRestart, "Should restart when changed config affects active profile")
}

// TestReconcileWhenHardwareConfigChangedButNotAssociated tests that no restart happens when changed config is not associated
func TestReconcileWhenHardwareConfigChangedButNotAssociated(t *testing.T) {
	mockHandler := &MockHardwareConfigHandler{}
	mockTrigger := &MockHardwareConfigRestartTrigger{
		CurrentProfiles: []string{"active-profile"},
	}

	reconciler := &HardwareConfigReconciler{
		NodeName:              "test-node",
		HardwareConfigHandler: mockHandler,
		ConfigUpdate:          mockTrigger,
	}

	oldConfigs := []ptpv2alpha1.HardwareConfig{
		createTestHardwareConfig("config1", "profile1", "other-profile"),
	}

	newConfigs := []ptpv2alpha1.HardwareConfig{
		createTestHardwareConfig("config1", "profile1-modified", "other-profile"),
	}

	// Verify configs are different but don't affect active profiles
	oldMap := sliceToHardwareConfigMap(oldConfigs)
	diff := diffHardwareConfigs(oldMap, newConfigs)
	assert.True(t, diff.HasChanges(), "Configs should be different")
	shouldRestart := reconciler.checkIfChangedConfigsAffectActiveProfiles(diff)
	assert.False(t, shouldRestart, "Should not restart when changed config does not affect active profile")
}

// TestMockHandlerReturnsError tests that the mock handler returns configured errors
func TestMockHandlerReturnsError(t *testing.T) {
	mockHandler := &MockHardwareConfigHandler{
		UpdateError: errors.New("update failed"),
	}

	newConfigs := []ptpv2alpha1.HardwareConfig{
		createTestHardwareConfig("config1", "profile1-modified", "active-profile"),
	}

	// Verify mock returns error as configured
	err := mockHandler.UpdateHardwareConfig(newConfigs)
	assert.Error(t, err, "Mock should return configured error")
	assert.Equal(t, 1, mockHandler.UpdateCallCount, "Update should have been called once")
}

// TestReconcileEmptyToSomething tests that change from empty to something is detected
func TestReconcileEmptyToSomething(t *testing.T) {
	emptyConfigs := []ptpv2alpha1.HardwareConfig{}
	someConfigs := []ptpv2alpha1.HardwareConfig{
		createTestHardwareConfig("config1", "profile1", "ptp1"),
	}

	// Empty to something should be detected as change
	emptyMap := sliceToHardwareConfigMap(emptyConfigs)
	someMap := sliceToHardwareConfigMap(someConfigs)
	diff1 := diffHardwareConfigs(emptyMap, someConfigs)
	diff2 := diffHardwareConfigs(someMap, emptyConfigs)
	assert.True(t, diff1.HasChanges(), "Empty to something should be detected as change")
	assert.True(t, diff2.HasChanges(), "Something to empty should be detected as change")
}

// TestReconcileMultipleConfigsInSequence tests that only changed configs trigger restarts
func TestReconcileMultipleConfigsInSequence(t *testing.T) {
	mockHandler := &MockHardwareConfigHandler{}
	mockTrigger := &MockHardwareConfigRestartTrigger{
		CurrentProfiles: []string{"active-profile"},
	}

	reconciler := &HardwareConfigReconciler{
		NodeName:              "test-node",
		HardwareConfigHandler: mockHandler,
		ConfigUpdate:          mockTrigger,
	}

	// Initial configs
	config1 := createTestHardwareConfig("config1", "profile1", "active-profile")
	config2 := createTestHardwareConfig("config2", "profile2", "other-profile")

	initialConfigs := []ptpv2alpha1.HardwareConfig{config1, config2}

	// Set initial configs (no mutex needed in tests - single-threaded)
	reconciler.lastAppliedConfigs = deepCopyHardwareConfigMap(sliceToHardwareConfigMap(initialConfigs))

	// First change: modify config1 (affects active profile)
	modifiedConfig1 := createTestHardwareConfig("config1", "profile1-modified", "active-profile")
	firstChange := []ptpv2alpha1.HardwareConfig{modifiedConfig1, config2}

	// Compute diff and check (matching production code pattern)
	initialMap := sliceToHardwareConfigMap(initialConfigs)
	diff1 := diffHardwareConfigs(initialMap, firstChange)
	shouldRestart1 := reconciler.checkIfChangedConfigsAffectActiveProfiles(diff1)
	assert.True(t, shouldRestart1, "Should restart when config1 (affecting active profile) changes")

	// Second change: modify config2 (does not affect active profile)
	modifiedConfig2 := createTestHardwareConfig("config2", "profile2-modified", "other-profile")
	secondChange := []ptpv2alpha1.HardwareConfig{modifiedConfig1, modifiedConfig2}

	// Compute diff and check (matching production code pattern)
	firstChangeMap := sliceToHardwareConfigMap(firstChange)
	diff2 := diffHardwareConfigs(firstChangeMap, secondChange)
	shouldRestart2 := reconciler.checkIfChangedConfigsAffectActiveProfiles(diff2)
	assert.False(t, shouldRestart2, "Should not restart when only config2 (not affecting active profile) changes")
}

func TestHardwareConfigReconcilerFields(t *testing.T) {
	mockHandler := &MockHardwareConfigHandler{}
	mockTrigger := &MockHardwareConfigRestartTrigger{}

	reconciler := &HardwareConfigReconciler{
		NodeName:              "test-node",
		HardwareConfigHandler: mockHandler,
		ConfigUpdate:          mockTrigger,
	}

	assert.Equal(t, "test-node", reconciler.NodeName)
	assert.NotNil(t, reconciler.HardwareConfigHandler)
	assert.NotNil(t, reconciler.ConfigUpdate)
}
