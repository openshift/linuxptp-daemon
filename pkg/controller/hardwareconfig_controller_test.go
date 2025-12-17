package controller

import (
	"context"
	"testing"
	"time"

	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"github.com/stretchr/testify/assert"
)

// MockHardwareConfigHandler implements HardwareConfigUpdateHandler for testing
type MockHardwareConfigHandler struct {
	LastUpdateConfigs []ptpv2alpha1.HardwareConfig
	UpdateCallCount   int
}

func (m *MockHardwareConfigHandler) UpdateHardwareConfig(hwConfigs []ptpv2alpha1.HardwareConfig) error {
	m.LastUpdateConfigs = hwConfigs
	m.UpdateCallCount++
	return nil
}

// MockHardwareConfigRestartTrigger implements HardwareConfigRestartTrigger for testing
type MockHardwareConfigRestartTrigger struct {
	RestartTriggerCount int
	CurrentProfiles     []string
}

func (m *MockHardwareConfigRestartTrigger) TriggerRestartForHardwareChange() error {
	m.RestartTriggerCount++
	return nil
}

func (m *MockHardwareConfigRestartTrigger) GetCurrentPTPProfiles() []string {
	return m.CurrentProfiles
}

func TestCalculateNodeHardwareConfigs(t *testing.T) {
	testCases := []struct {
		name                 string
		nodeName             string
		hwConfigs            []ptpv2alpha1.HardwareConfig
		expectedConfigsCount int
		expectedConfigNames  []string
	}{
		{
			name:                 "no hardware configs",
			nodeName:             "test-node",
			hwConfigs:            []ptpv2alpha1.HardwareConfig{},
			expectedConfigsCount: 0,
			expectedConfigNames:  []string{},
		},
		{
			name:     "single hardware config with grandmaster profile",
			nodeName: "test-node",
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				{
					Spec: ptpv2alpha1.HardwareConfigSpec{
						Profile: ptpv2alpha1.HardwareProfile{
							Name:        stringPtr("grandmaster-profile"),
							Description: stringPtr("High-precision grandmaster configuration"),
							ClockChain: &ptpv2alpha1.ClockChain{
								Structure: []ptpv2alpha1.Subsystem{
									{
										Name:                        "primary-subsystem",
										HardwareSpecificDefinitions: "intel/e810",
										DPLL: ptpv2alpha1.DPLL{
											NetworkInterface: "ens1f0",
										},
										Ethernet: []ptpv2alpha1.Ethernet{
											{
												Ports: []string{"ens1f0", "ens1f1"},
											},
										},
									},
								},
							},
						},
						RelatedPtpProfileName: "grandmaster",
					},
				},
			},
			expectedConfigsCount: 1,
			expectedConfigNames:  []string{"grandmaster-profile"},
		},
		{
			name:     "multiple hardware configs",
			nodeName: "worker-node",
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				{
					Spec: ptpv2alpha1.HardwareConfigSpec{
						Profile: ptpv2alpha1.HardwareProfile{
							Name:        stringPtr("boundary-clock-profile"),
							Description: stringPtr("Boundary clock configuration"),
							ClockChain: &ptpv2alpha1.ClockChain{
								Structure: []ptpv2alpha1.Subsystem{
									{
										Name: "bc-subsystem",
										DPLL: ptpv2alpha1.DPLL{
											NetworkInterface: "ens2f0",
										},
										Ethernet: []ptpv2alpha1.Ethernet{
											{
												Ports: []string{"ens2f0"},
											},
										},
									},
								},
							},
						},
						RelatedPtpProfileName: "boundary-clock",
					},
				},
				{
					Spec: ptpv2alpha1.HardwareConfigSpec{
						Profile: ptpv2alpha1.HardwareProfile{
							Name:        stringPtr("ordinary-clock-profile"),
							Description: stringPtr("Ordinary clock configuration"),
							ClockChain: &ptpv2alpha1.ClockChain{
								Structure: []ptpv2alpha1.Subsystem{
									{
										Name: "oc-subsystem",
										DPLL: ptpv2alpha1.DPLL{
											NetworkInterface: "ens3f0",
										},
										Ethernet: []ptpv2alpha1.Ethernet{
											{
												Ports: []string{"ens3f0"},
											},
										},
									},
								},
							},
						},
						RelatedPtpProfileName: "ordinary-clock",
					},
				},
			},
			expectedConfigsCount: 2,
			expectedConfigNames:  []string{"boundary-clock-profile", "ordinary-clock-profile"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			reconciler := &HardwareConfigReconciler{
				NodeName: tc.nodeName,
			}

			// Call the method under test
			result, err := reconciler.calculateNodeHardwareConfigs(context.TODO(), tc.hwConfigs)

			// Verify no error occurred
			assert.NoError(t, err)

			// Verify the number of hardware configs
			assert.Len(t, result, tc.expectedConfigsCount,
				"Expected %d hardware configs, got %d", tc.expectedConfigsCount, len(result))

			// Verify config names match expected (based on profile names within configs)
			var actualConfigNames []string
			for _, hwConfig := range result {
				if hwConfig.Spec.Profile.Name != nil {
					actualConfigNames = append(actualConfigNames, *hwConfig.Spec.Profile.Name)
				} else {
					actualConfigNames = append(actualConfigNames, "unnamed")
				}
			}
			assert.ElementsMatch(t, tc.expectedConfigNames, actualConfigNames,
				"Expected config names %v, got %v", tc.expectedConfigNames, actualConfigNames)

			// Additional validations
			for i, hwConfig := range result {
				profile := hwConfig.Spec.Profile
				if profile.Name != nil {
					assert.NotEmpty(t, *profile.Name, "Profile name should not be empty for config %d", i)
				}
				assert.NotNil(t, profile.ClockChain, "ClockChain should not be nil for config %d", i)
				if profile.ClockChain != nil {
					assert.NotEmpty(t, profile.ClockChain.Structure, "ClockChain structure should not be empty for config %d", i)
				}
			}
		})
	}
}

func TestHardwareConfigUpdateHandlerIntegration(t *testing.T) {
	// Test the interaction between controller and handler
	mockHandler := &MockHardwareConfigHandler{}

	reconciler := &HardwareConfigReconciler{
		NodeName:              "test-node", //nolint:govet // needed for test setup
		HardwareConfigHandler: mockHandler,
	}

	// Create some test hardware configs
	testConfigs := []ptpv2alpha1.HardwareConfig{
		{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				Profile: ptpv2alpha1.HardwareProfile{
					Name:        stringPtr("ordinary-clock-profile"),
					Description: stringPtr("Test ordinary clock configuration"),
					ClockChain: &ptpv2alpha1.ClockChain{
						Structure: []ptpv2alpha1.Subsystem{
							{
								Name: "oc-subsystem",
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
				RelatedPtpProfileName: "ordinary-clock",
			},
		},
		{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				Profile: ptpv2alpha1.HardwareProfile{
					Name:        stringPtr("grandmaster-profile"),
					Description: stringPtr("Test grandmaster configuration"),
					ClockChain: &ptpv2alpha1.ClockChain{
						Structure: []ptpv2alpha1.Subsystem{
							{
								Name:                        "gm-subsystem",
								HardwareSpecificDefinitions: "intel/e810",
								DPLL: ptpv2alpha1.DPLL{
									NetworkInterface: "ens2f0",
								},
								Ethernet: []ptpv2alpha1.Ethernet{
									{
										Ports: []string{"ens2f0", "ens2f1"},
									},
								},
							},
						},
					},
				},
				RelatedPtpProfileName: "grandmaster",
			},
		},
	}

	// Call UpdateHardwareConfig through the handler
	err := reconciler.HardwareConfigHandler.UpdateHardwareConfig(testConfigs)

	// Verify the call succeeded
	assert.NoError(t, err)

	// Verify the handler received the correct configurations
	assert.Equal(t, 1, mockHandler.UpdateCallCount, "Handler should have been called exactly once")
	assert.Len(t, mockHandler.LastUpdateConfigs, 2, "Handler should have received 2 hardware configs")

	// Verify the specific profiles
	assert.Equal(t, "ordinary-clock-profile", *mockHandler.LastUpdateConfigs[0].Spec.Profile.Name)
	assert.Equal(t, "Test ordinary clock configuration", *mockHandler.LastUpdateConfigs[0].Spec.Profile.Description)
	assert.NotNil(t, mockHandler.LastUpdateConfigs[0].Spec.Profile.ClockChain)

	assert.Equal(t, "grandmaster-profile", *mockHandler.LastUpdateConfigs[1].Spec.Profile.Name)
	assert.Equal(t, "Test grandmaster configuration", *mockHandler.LastUpdateConfigs[1].Spec.Profile.Description)
	assert.NotNil(t, mockHandler.LastUpdateConfigs[1].Spec.Profile.ClockChain)
}

func TestCheckIfActiveProfilesAffected(t *testing.T) {
	testCases := []struct {
		name            string
		activeProfiles  []string
		hwConfigs       []ptpv2alpha1.HardwareConfig
		expectedRestart bool
		description     string
	}{
		{
			name:            "no active profiles",
			activeProfiles:  []string{},
			hwConfigs:       []ptpv2alpha1.HardwareConfig{},
			expectedRestart: false,
			description:     "Should not restart when no active profiles exist",
		},
		{
			name:            "no hardware configs",
			activeProfiles:  []string{"grandmaster-profile"},
			hwConfigs:       []ptpv2alpha1.HardwareConfig{},
			expectedRestart: false,
			description:     "Should not restart when no hardware configs exist",
		},
		{
			name:           "hardware config associated with active profile",
			activeProfiles: []string{"grandmaster-profile", "boundary-clock-profile"},
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				{
					Spec: ptpv2alpha1.HardwareConfigSpec{
						RelatedPtpProfileName: "grandmaster-profile",
						Profile: ptpv2alpha1.HardwareProfile{
							Name: stringPtr("intel-e810-gm"),
						},
					},
				},
			},
			expectedRestart: true,
			description:     "Should restart when hardware config is associated with active profile",
		},
		{
			name:           "hardware config not associated with active profile",
			activeProfiles: []string{"ordinary-clock-profile"},
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				{
					Spec: ptpv2alpha1.HardwareConfigSpec{
						RelatedPtpProfileName: "grandmaster-profile",
						Profile: ptpv2alpha1.HardwareProfile{
							Name: stringPtr("intel-e810-gm"),
						},
					},
				},
			},
			expectedRestart: false,
			description:     "Should not restart when hardware config is not associated with any active profile",
		},
		{
			name:           "multiple hardware configs, one matches",
			activeProfiles: []string{"boundary-clock-profile"},
			hwConfigs: []ptpv2alpha1.HardwareConfig{
				{
					Spec: ptpv2alpha1.HardwareConfigSpec{
						RelatedPtpProfileName: "grandmaster-profile",
						Profile: ptpv2alpha1.HardwareProfile{
							Name: stringPtr("intel-e810-gm"),
						},
					},
				},
				{
					Spec: ptpv2alpha1.HardwareConfigSpec{
						RelatedPtpProfileName: "boundary-clock-profile",
						Profile: ptpv2alpha1.HardwareProfile{
							Name: stringPtr("intel-e810-bc"),
						},
					},
				},
			},
			expectedRestart: true,
			description:     "Should restart when at least one hardware config is associated with active profile",
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

			result := reconciler.checkIfActiveProfilesAffected(context.TODO(), tc.hwConfigs)

			assert.Equal(t, tc.expectedRestart, result, tc.description)
		})
	}
}

func TestRestartTriggerIntegration(t *testing.T) {
	// Test the complete flow of hardware config change triggering deferred PTP restart
	mockHandler := &MockHardwareConfigHandler{}
	mockTrigger := &MockHardwareConfigRestartTrigger{
		CurrentProfiles: []string{"grandmaster-profile"},
	}

	reconciler := &HardwareConfigReconciler{
		NodeName:              "test-node",
		HardwareConfigHandler: mockHandler,
		ConfigUpdate:          mockTrigger,
	}

	// Create a hardware config that is associated with an active profile
	hwConfigs := []ptpv2alpha1.HardwareConfig{
		{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: "grandmaster-profile",
				Profile: ptpv2alpha1.HardwareProfile{
					Name:        stringPtr("intel-e810-gm"),
					Description: stringPtr("Intel E810 grandmaster configuration"),
				},
			},
		},
	}

	// Test that the restart is needed
	needsRestart := reconciler.checkIfActiveProfilesAffected(context.TODO(), hwConfigs)
	assert.True(t, needsRestart, "Should detect that restart is needed")

	// Test the deferred restart mechanism
	reconciler.scheduleDeferredRestart(context.TODO())

	// Wait for the deferred restart to execute (waits for active reconciliations to complete)
	// Since there are no active reconciliations, it should trigger immediately
	// Use a small retry loop to wait for the goroutine to complete
	for i := 0; i < 10; i++ {
		if mockTrigger.RestartTriggerCount > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify that the restart was triggered after waiting for reconciliations
	assert.Equal(t, 1, mockTrigger.RestartTriggerCount, "Restart should have been triggered once after reconciliations complete")
}

// Test that multiple restart requests are handled properly
func TestDeferredRestartDebouncing(t *testing.T) {
	mockHandler := &MockHardwareConfigHandler{}
	mockTrigger := &MockHardwareConfigRestartTrigger{
		CurrentProfiles: []string{"grandmaster-profile"},
	}

	reconciler := &HardwareConfigReconciler{
		NodeName:              "test-node",
		HardwareConfigHandler: mockHandler,
		ConfigUpdate:          mockTrigger,
	}

	// Schedule the first deferred restart
	reconciler.scheduleDeferredRestart(context.TODO())

	// Wait a bit to ensure the first restart is scheduled
	time.Sleep(10 * time.Millisecond)

	// Schedule additional deferred restarts - these should be debounced
	reconciler.scheduleDeferredRestart(context.TODO())
	reconciler.scheduleDeferredRestart(context.TODO())

	// Wait for the goroutine to complete (waits for active reconciliations)
	// Use a small retry loop to wait for the goroutine to complete
	for i := 0; i < 10; i++ {
		if mockTrigger.RestartTriggerCount > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Only the first restart should have been triggered due to debouncing
	assert.Equal(t, 1, mockTrigger.RestartTriggerCount, "Only one restart should have been triggered due to debouncing")
}

// Test that restart waits for active reconciliations to complete
func TestDeferredRestartWaitsForReconciliations(t *testing.T) {
	mockHandler := &MockHardwareConfigHandler{}
	mockTrigger := &MockHardwareConfigRestartTrigger{
		CurrentProfiles: []string{"grandmaster-profile"},
	}

	reconciler := &HardwareConfigReconciler{
		NodeName:              "test-node",
		HardwareConfigHandler: mockHandler,
		ConfigUpdate:          mockTrigger,
	}

	// Start a reconciliation (simulate active reconciliation)
	reconciler.reconciliationMutex.Lock()
	reconciler.activeReconciliations.Add(1)
	reconciler.reconciliationMutex.Unlock()

	// Schedule restart - it should wait for the active reconciliation
	reconciler.scheduleDeferredRestart(context.TODO())

	// Verify restart hasn't been triggered yet (reconciliation still active)
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, mockTrigger.RestartTriggerCount, "Restart should not have been triggered while reconciliation is active")

	// Complete the reconciliation
	reconciler.reconciliationMutex.Lock()
	reconciler.activeReconciliations.Done()
	reconciler.reconciliationMutex.Unlock()

	// Wait for the restart to be triggered now that reconciliation is complete
	for i := 0; i < 10; i++ {
		if mockTrigger.RestartTriggerCount > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify that the restart was triggered after reconciliation completed
	assert.Equal(t, 1, mockTrigger.RestartTriggerCount, "Restart should have been triggered after reconciliation completed")
}

func TestHardwareConfigReconcilerFields(t *testing.T) {
	mockHandler := &MockHardwareConfigHandler{}
	mockTrigger := &MockHardwareConfigRestartTrigger{}

	reconciler := &HardwareConfigReconciler{
		NodeName:              "test-node",
		HardwareConfigHandler: mockHandler,
		ConfigUpdate:          mockTrigger,
	}

	// Verify reconciler has all required fields
	assert.Equal(t, "test-node", reconciler.NodeName)
	assert.NotNil(t, reconciler.HardwareConfigHandler)
	assert.NotNil(t, reconciler.ConfigUpdate)

	// Verify the handler implements the interface
	var _ = reconciler.HardwareConfigHandler
	var _ = reconciler.ConfigUpdate
}
