package hardwareconfig

import (
	"testing"

	"github.com/stretchr/testify/assert"

	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

func TestResolvePhaseAdjustments(t *testing.T) {
	tests := []struct {
		name                string
		hwDefaults          *HardwareDefaults
		networkInterface    string
		expectedCount       int
		expectedAdjustments map[string]int64
		expectError         bool
	}{
		{
			name: "basic route resolution",
			hwDefaults: &HardwareDefaults{
				DelayCompensation: &DelayCompensationModel{
					Components: []Component{
						{ID: "DPLL 1kHz input", CompensationPoint: &CompensationPoint{Name: "GNR-D_SDP2", Type: "dpll"}},
					},
					Connections: []Connection{
						{From: "NAC0 PTP phase output", To: "DPLL 1kHz input", DelayPs: 8750},
					},
					Routes: []Route{
						{
							Name:        "NAC PTP to DPLL",
							Sequence:    []string{"NAC0 PTP phase output", "DPLL 1kHz input"},
							Compensator: "DPLL 1kHz input",
						},
					},
				},
			},
			networkInterface: "ens4f0",
			expectedCount:    1,
			expectedAdjustments: map[string]int64{
				"GNR-D_SDP2": -8750, // Negated delay (adjustment)
			},
		},
		{
			name: "multiple routes",
			hwDefaults: &HardwareDefaults{
				DelayCompensation: &DelayCompensationModel{
					Components: []Component{
						{ID: "DPLL 1kHz input", CompensationPoint: &CompensationPoint{Name: "GNR-D_SDP2", Type: "dpll"}},
						{ID: "DPLL ePPS output 1", CompensationPoint: &CompensationPoint{Name: "OCP1_CLK", Type: "dpll"}},
					},
					Connections: []Connection{
						{From: "NAC0 PTP phase output", To: "DPLL 1kHz input", DelayPs: 8750},
						{From: "DPLL ePPS output 1", To: "CF1 ePPS input", DelayPs: 10000},
					},
					Routes: []Route{
						{
							Name:        "NAC PTP to DPLL",
							Sequence:    []string{"NAC0 PTP phase output", "DPLL 1kHz input"},
							Compensator: "DPLL 1kHz input",
						},
						{
							Name:        "DPLL to CF1",
							Sequence:    []string{"DPLL ePPS output 1", "CF1 ePPS input"},
							Compensator: "DPLL ePPS output 1",
						},
					},
				},
			},
			networkInterface: "ens4f0",
			expectedCount:    2,
			expectedAdjustments: map[string]int64{
				"GNR-D_SDP2": -8750,  // Negated delay (adjustment)
				"OCP1_CLK":   -10000, // Negated delay (adjustment)
			},
		},
		{
			name:             "no delay compensation model",
			hwDefaults:       &HardwareDefaults{DelayCompensation: nil},
			networkInterface: "ens4f0",
			expectedCount:    0,
		},
		{
			name:             "nil hardware defaults",
			hwDefaults:       nil,
			networkInterface: "ens4f0",
			expectedCount:    0,
		},
		{
			name: "route with missing connection",
			hwDefaults: &HardwareDefaults{
				DelayCompensation: &DelayCompensationModel{
					Components: []Component{
						{ID: "DPLL 1kHz input", CompensationPoint: &CompensationPoint{Name: "GNR-D_SDP2", Type: "dpll"}},
					},
					Connections: []Connection{
						// Missing connection for route
					},
					Routes: []Route{
						{
							Name:        "NAC PTP to DPLL",
							Sequence:    []string{"NAC0 PTP phase output", "DPLL 1kHz input"},
							Compensator: "DPLL 1kHz input",
						},
					},
				},
			},
			networkInterface: "ens4f0",
			expectedCount:    0, // Route should be skipped due to missing connection
		},
		{
			name: "route with missing compensator component",
			hwDefaults: &HardwareDefaults{
				DelayCompensation: &DelayCompensationModel{
					Components: []Component{
						// Missing compensator component
					},
					Connections: []Connection{
						{From: "NAC0 PTP phase output", To: "DPLL 1kHz input", DelayPs: 8750},
					},
					Routes: []Route{
						{
							Name:        "NAC PTP to DPLL",
							Sequence:    []string{"NAC0 PTP phase output", "DPLL 1kHz input"},
							Compensator: "DPLL 1kHz input",
						},
					},
				},
			},
			networkInterface: "ens4f0",
			expectedCount:    0, // Route should be skipped due to missing component
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ResolvePhaseAdjustments(tt.hwDefaults, tt.networkInterface)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedCount, len(result))

			if tt.expectedAdjustments != nil {
				for boardLabel, expected := range tt.expectedAdjustments {
					actual, found := result[boardLabel]
					assert.True(t, found, "Expected adjustment for pin %s not found", boardLabel)
					if found {
						assert.Equal(t, expected, actual)
					}
				}
			}
		})
	}
}

func TestCalculateRouteDelay(t *testing.T) {
	tests := []struct {
		name             string
		model            *DelayCompensationModel
		sequence         []string
		networkInterface string
		expectedDelay    int64
		expectError      bool
	}{
		{
			name: "single connection route",
			model: &DelayCompensationModel{
				Connections: []Connection{
					{From: "NAC0 PTP phase output", To: "DPLL 1kHz input", DelayPs: 8750},
				},
			},
			sequence:         []string{"NAC0 PTP phase output", "DPLL 1kHz input"},
			networkInterface: "ens4f0",
			expectedDelay:    8750,
		},
		{
			name: "multiple connections route",
			model: &DelayCompensationModel{
				Connections: []Connection{
					{From: "A", To: "B", DelayPs: 1000},
					{From: "B", To: "C", DelayPs: 2000},
				},
			},
			sequence:         []string{"A", "B", "C"},
			networkInterface: "ens4f0",
			expectedDelay:    3000,
		},
		{
			name: "route with positive delays",
			model: &DelayCompensationModel{
				Connections: []Connection{
					{From: "DPLL ePPS output 1", To: "CF1 ePPS input", DelayPs: 10000},
				},
			},
			sequence:         []string{"DPLL ePPS output 1", "CF1 ePPS input"},
			networkInterface: "ens4f0",
			expectedDelay:    10000, // Delays are positive, calculateRouteDelay returns raw sum
		},
		{
			name: "missing connection",
			model: &DelayCompensationModel{
				Connections: []Connection{
					// Missing connection A->B
				},
			},
			sequence:         []string{"A", "B"},
			networkInterface: "ens4f0",
			expectError:      true,
		},
		{
			name: "invalid sequence (too short)",
			model: &DelayCompensationModel{
				Connections: []Connection{
					{From: "A", To: "B", DelayPs: 1000},
				},
			},
			sequence:         []string{"A"}, // Need at least 2 components
			networkInterface: "ens4f0",
			expectError:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay, err := calculateRouteDelay(tt.model, tt.sequence, tt.networkInterface)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedDelay, delay)
		})
	}
}

func TestGetConnectionDelay(t *testing.T) {
	tests := []struct {
		name             string
		model            *DelayCompensationModel
		from             string
		to               string
		networkInterface string
		expectedDelay    int64
		expectError      bool
		setupFS          func(string) // Setup filesystem mock
		cleanupFS        func()       // Cleanup filesystem mock
	}{
		{
			name: "connection with default delay only",
			model: &DelayCompensationModel{
				Connections: []Connection{
					{From: "A", To: "B", DelayPs: 5000},
				},
			},
			from:             "A",
			to:               "B",
			networkInterface: "ens4f0",
			expectedDelay:    5000,
		},
		{
			name: "connection with FSRead getter (file exists but not implemented)",
			model: &DelayCompensationModel{
				Connections: []Connection{
					{
						From:    "A",
						To:      "B",
						DelayPs: 5000, // Fallback
						Getter: &DelayGetter{
							FSRead: &FSReadGetter{
								Path: "/tmp/test-delay",
							},
						},
					},
				},
			},
			from:             "A",
			to:               "B",
			networkInterface: "ens4f0",
			expectedDelay:    5000, // Should use fallback since FSRead is not implemented
		},
		{
			name: "connection not found",
			model: &DelayCompensationModel{
				Connections: []Connection{
					{From: "A", To: "B", DelayPs: 5000},
				},
			},
			from:             "X",
			to:               "Y",
			networkInterface: "ens4f0",
			expectError:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFS != nil {
				tt.setupFS("/tmp")
			}
			defer func() {
				if tt.cleanupFS != nil {
					tt.cleanupFS()
				}
			}()

			delay, err := getConnectionDelay(tt.model, tt.from, tt.to, tt.networkInterface)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedDelay, delay)
		})
	}
}

func TestPopulatePhaseAdjustmentsFromDelays(t *testing.T) {
	tests := []struct {
		name             string
		hwConfig         *ptpv2alpha1.HardwareConfig
		hwDefaults       *HardwareDefaults
		networkInterface string
		expectError      bool
		verifyFunc       func(*testing.T, *ptpv2alpha1.HardwareConfig)
	}{
		{
			name: "populate phase adjustment in existing pin config (PhaseOutputs)",
			hwConfig: &ptpv2alpha1.HardwareConfig{
				Spec: ptpv2alpha1.HardwareConfigSpec{
					Profile: ptpv2alpha1.HardwareProfile{
						ClockChain: &ptpv2alpha1.ClockChain{
							Structure: []ptpv2alpha1.Subsystem{
								{
									DPLL: ptpv2alpha1.DPLL{
										PhaseOutputs: map[string]ptpv2alpha1.PinConfig{
											"OCP1_CLK": {},
										},
									},
								},
							},
						},
					},
				},
			},
			hwDefaults: &HardwareDefaults{
				DelayCompensation: &DelayCompensationModel{
					Components: []Component{
						{ID: "DPLL ePPS output 1", CompensationPoint: &CompensationPoint{Name: "OCP1_CLK", Type: "dpll"}},
					},
					Connections: []Connection{
						{From: "DPLL ePPS output 1", To: "CF1 ePPS input", DelayPs: 10000},
					},
					Routes: []Route{
						{
							Name:        "DPLL to CF1",
							Sequence:    []string{"DPLL ePPS output 1", "CF1 ePPS input"},
							Compensator: "DPLL ePPS output 1",
						},
					},
				},
			},
			networkInterface: "ens4f0",
			verifyFunc: func(t *testing.T, hwConfig *ptpv2alpha1.HardwareConfig) {
				pinConfig := hwConfig.Spec.Profile.ClockChain.Structure[0].DPLL.PhaseOutputs["OCP1_CLK"]
				assert.NotNil(t, pinConfig.PhaseAdjustment)
				// Internal adjustment is -10000 (already negated from delay), user adjustment is 0, so total is -10000
				assert.Equal(t, int64(-10000), *pinConfig.PhaseAdjustment)
			},
		},
		{
			name: "preserve existing user adjustment",
			hwConfig: &ptpv2alpha1.HardwareConfig{
				Spec: ptpv2alpha1.HardwareConfigSpec{
					Profile: ptpv2alpha1.HardwareProfile{
						ClockChain: &ptpv2alpha1.ClockChain{
							Structure: []ptpv2alpha1.Subsystem{
								{
									DPLL: ptpv2alpha1.DPLL{
										PhaseOutputs: map[string]ptpv2alpha1.PinConfig{
											"OCP1_CLK": {
												PhaseAdjustment: int64Ptr(-5000), // User-specified adjustment
											},
										},
									},
								},
							},
						},
					},
				},
			},
			hwDefaults: &HardwareDefaults{
				DelayCompensation: &DelayCompensationModel{
					Components: []Component{
						{ID: "DPLL ePPS output 1", CompensationPoint: &CompensationPoint{Name: "OCP1_CLK", Type: "dpll"}},
					},
					Connections: []Connection{
						{From: "DPLL ePPS output 1", To: "CF1 ePPS input", DelayPs: 10000},
					},
					Routes: []Route{
						{
							Name:        "DPLL to CF1",
							Sequence:    []string{"DPLL ePPS output 1", "CF1 ePPS input"},
							Compensator: "DPLL ePPS output 1",
						},
					},
				},
			},
			networkInterface: "ens4f0",
			verifyFunc: func(t *testing.T, hwConfig *ptpv2alpha1.HardwareConfig) {
				pinConfig := hwConfig.Spec.Profile.ClockChain.Structure[0].DPLL.PhaseOutputs["OCP1_CLK"]
				assert.NotNil(t, pinConfig.PhaseAdjustment)
				// Internal adjustment is -10000 (already negated from delay), user adjustment is -5000, so total is -15000
				assert.Equal(t, int64(-15000), *pinConfig.PhaseAdjustment)
			},
		},
		{
			name: "no delay compensation model",
			hwConfig: &ptpv2alpha1.HardwareConfig{
				Spec: ptpv2alpha1.HardwareConfigSpec{
					Profile: ptpv2alpha1.HardwareProfile{
						ClockChain: &ptpv2alpha1.ClockChain{
							Structure: []ptpv2alpha1.Subsystem{
								{
									DPLL: ptpv2alpha1.DPLL{
										PhaseOutputs: map[string]ptpv2alpha1.PinConfig{
											"OCP1_CLK": {},
										},
									},
								},
							},
						},
					},
				},
			},
			hwDefaults: &HardwareDefaults{
				DelayCompensation: nil,
			},
			networkInterface: "ens4f0",
			verifyFunc: func(t *testing.T, hwConfig *ptpv2alpha1.HardwareConfig) {
				// Should not modify pin config
				pinConfig := hwConfig.Spec.Profile.ClockChain.Structure[0].DPLL.PhaseOutputs["OCP1_CLK"]
				assert.Nil(t, pinConfig.PhaseAdjustment)
			},
		},
		{
			name: "pin not in PhaseOutputs (should be skipped)",
			hwConfig: &ptpv2alpha1.HardwareConfig{
				Spec: ptpv2alpha1.HardwareConfigSpec{
					Profile: ptpv2alpha1.HardwareProfile{
						ClockChain: &ptpv2alpha1.ClockChain{
							Structure: []ptpv2alpha1.Subsystem{
								{
									DPLL: ptpv2alpha1.DPLL{
										PhaseOutputs: map[string]ptpv2alpha1.PinConfig{
											"OTHER_PIN": {},
										},
									},
								},
							},
						},
					},
				},
			},
			hwDefaults: &HardwareDefaults{
				DelayCompensation: &DelayCompensationModel{
					Components: []Component{
						{ID: "DPLL ePPS output 1", CompensationPoint: &CompensationPoint{Name: "OCP1_CLK", Type: "dpll"}},
					},
					Connections: []Connection{
						{From: "DPLL ePPS output 1", To: "CF1 ePPS input", DelayPs: 10000},
					},
					Routes: []Route{
						{
							Name:        "DPLL to CF1",
							Sequence:    []string{"DPLL ePPS output 1", "CF1 ePPS input"},
							Compensator: "DPLL ePPS output 1",
						},
					},
				},
			},
			networkInterface: "ens4f0",
			verifyFunc: func(t *testing.T, hwConfig *ptpv2alpha1.HardwareConfig) {
				// OCP1_CLK adjustment should not be populated since pin doesn't exist
				pinConfig := hwConfig.Spec.Profile.ClockChain.Structure[0].DPLL.PhaseOutputs["OTHER_PIN"]
				assert.Nil(t, pinConfig.PhaseAdjustment)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := PopulatePhaseAdjustmentsFromDelays(tt.hwConfig, tt.hwDefaults, tt.networkInterface)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			if tt.verifyFunc != nil {
				tt.verifyFunc(t, tt.hwConfig)
			}
		})
	}
}

// Helper functions
func int64Ptr(i int64) *int64 {
	return &i
}
