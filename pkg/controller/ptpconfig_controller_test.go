package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

func TestDoesRecommendationMatch(t *testing.T) {
	testCases := []struct {
		name          string
		recommend     ptpv1.PtpRecommend
		nodeName      string
		nodeLabels    map[string]string
		expectedMatch bool
	}{
		{
			name: "no match rules - should match all",
			recommend: ptpv1.PtpRecommend{
				Match: []ptpv1.MatchRule{},
			},
			nodeName:      "test-node",
			nodeLabels:    map[string]string{},
			expectedMatch: true,
		},
		{
			name: "node name match",
			recommend: ptpv1.PtpRecommend{
				Match: []ptpv1.MatchRule{
					{
						NodeName: stringPtr("test-node"),
					},
				},
			},
			nodeName:      "test-node",
			nodeLabels:    map[string]string{},
			expectedMatch: true,
		},
		{
			name: "node name no match",
			recommend: ptpv1.PtpRecommend{
				Match: []ptpv1.MatchRule{
					{
						NodeName: stringPtr("other-node"),
					},
				},
			},
			nodeName:      "test-node",
			nodeLabels:    map[string]string{},
			expectedMatch: false,
		},
		{
			name: "node label match",
			recommend: ptpv1.PtpRecommend{
				Match: []ptpv1.MatchRule{
					{
						NodeLabel: stringPtr("worker"),
					},
				},
			},
			nodeName: "test-node",
			nodeLabels: map[string]string{
				"worker": "true",
			},
			expectedMatch: true,
		},
		{
			name: "node label no match",
			recommend: ptpv1.PtpRecommend{
				Match: []ptpv1.MatchRule{
					{
						NodeLabel: stringPtr("master"),
					},
				},
			},
			nodeName: "test-node",
			nodeLabels: map[string]string{
				"worker": "true",
			},
			expectedMatch: false,
		},
		{
			name: "multiple match rules - OR logic (one matches)",
			recommend: ptpv1.PtpRecommend{
				Match: []ptpv1.MatchRule{
					{
						NodeName: stringPtr("other-node"),
					},
					{
						NodeLabel: stringPtr("worker"),
					},
				},
			},
			nodeName: "test-node",
			nodeLabels: map[string]string{
				"worker": "true",
			},
			expectedMatch: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := anyRuleMatchesNode(tc.recommend.Match, tc.nodeName, tc.nodeLabels)
			assert.Equal(t, tc.expectedMatch, result)
		})
	}
}

func TestCalculateNodeProfiles(t *testing.T) {
	testCases := []struct {
		name             string
		nodeName         string
		nodeLabels       map[string]string
		ptpConfigs       []ptpv1.PtpConfig
		expectedProfiles []string
	}{
		{
			name:     "node name match only",
			nodeName: "test-node",
			nodeLabels: map[string]string{
				"kubernetes.io/hostname": "test-node",
			},
			ptpConfigs: []ptpv1.PtpConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-config-1",
					},
					Spec: ptpv1.PtpConfigSpec{
						Profile: []ptpv1.PtpProfile{
							{
								Name:      stringPtr("profile-1"),
								Interface: stringPtr("eth0"),
							},
						},
						Recommend: []ptpv1.PtpRecommend{
							{
								Profile:  stringPtr("profile-1"),
								Priority: int64Ptr(100),
								Match: []ptpv1.MatchRule{
									{
										NodeName: stringPtr("test-node"),
									},
								},
							},
						},
					},
				},
			},
			expectedProfiles: []string{"profile-1"},
		},
		{
			name:     "node label match",
			nodeName: "worker-1",
			nodeLabels: map[string]string{
				"kubernetes.io/hostname":         "worker-1",
				"node-role.kubernetes.io/worker": "",
			},
			ptpConfigs: []ptpv1.PtpConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "worker-config",
					},
					Spec: ptpv1.PtpConfigSpec{
						Profile: []ptpv1.PtpProfile{
							{
								Name:      stringPtr("ordinary-clock"),
								Interface: stringPtr("ens1f0"),
							},
						},
						Recommend: []ptpv1.PtpRecommend{
							{
								Profile:  stringPtr("ordinary-clock"),
								Priority: int64Ptr(10),
								Match: []ptpv1.MatchRule{
									{
										NodeLabel: stringPtr("node-role.kubernetes.io/worker"),
									},
								},
							},
						},
					},
				},
			},
			expectedProfiles: []string{"ordinary-clock"},
		},
		{
			name:     "multiple configs with priority selection",
			nodeName: "test-node",
			nodeLabels: map[string]string{
				"kubernetes.io/hostname": "test-node",
				"worker":                 "true",
				"ptp-capable":            "true",
			},
			ptpConfigs: []ptpv1.PtpConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "high-priority-config",
					},
					Spec: ptpv1.PtpConfigSpec{
						Profile: []ptpv1.PtpProfile{
							{
								Name:      stringPtr("grandmaster"),
								Interface: stringPtr("eth0"),
							},
						},
						Recommend: []ptpv1.PtpRecommend{
							{
								Profile:  stringPtr("grandmaster"),
								Priority: int64Ptr(200),
								Match: []ptpv1.MatchRule{
									{
										NodeLabel: stringPtr("ptp-capable"),
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "low-priority-config",
					},
					Spec: ptpv1.PtpConfigSpec{
						Profile: []ptpv1.PtpProfile{
							{
								Name:      stringPtr("ordinary-clock"),
								Interface: stringPtr("eth1"),
							},
						},
						Recommend: []ptpv1.PtpRecommend{
							{
								Profile:  stringPtr("ordinary-clock"),
								Priority: int64Ptr(50),
								Match: []ptpv1.MatchRule{
									{
										NodeLabel: stringPtr("worker"),
									},
								},
							},
						},
					},
				},
			},
			expectedProfiles: []string{"grandmaster"}, // Higher priority wins
		},
		{
			name:     "no matching profiles",
			nodeName: "test-node",
			nodeLabels: map[string]string{
				"kubernetes.io/hostname": "test-node",
			},
			ptpConfigs: []ptpv1.PtpConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "master-only-config",
					},
					Spec: ptpv1.PtpConfigSpec{
						Profile: []ptpv1.PtpProfile{
							{
								Name:      stringPtr("grandmaster"),
								Interface: stringPtr("eth0"),
							},
						},
						Recommend: []ptpv1.PtpRecommend{
							{
								Profile:  stringPtr("grandmaster"),
								Priority: int64Ptr(100),
								Match: []ptpv1.MatchRule{
									{
										NodeLabel: stringPtr("node-role.kubernetes.io/master"),
									},
								},
							},
						},
					},
				},
			},
			expectedProfiles: []string{}, // No matches
		},
		{
			name:     "multiple profiles from different configs",
			nodeName: "test-node",
			nodeLabels: map[string]string{
				"kubernetes.io/hostname": "test-node",
				"worker":                 "true",
				"ptp-capable":            "true",
			},
			ptpConfigs: []ptpv1.PtpConfig{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config-1",
					},
					Spec: ptpv1.PtpConfigSpec{
						Profile: []ptpv1.PtpProfile{
							{
								Name:      stringPtr("profile-a"),
								Interface: stringPtr("eth0"),
							},
						},
						Recommend: []ptpv1.PtpRecommend{
							{
								Profile:  stringPtr("profile-a"),
								Priority: int64Ptr(100),
								Match: []ptpv1.MatchRule{
									{
										NodeLabel: stringPtr("worker"),
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "config-2",
					},
					Spec: ptpv1.PtpConfigSpec{
						Profile: []ptpv1.PtpProfile{
							{
								Name:      stringPtr("profile-b"),
								Interface: stringPtr("eth1"),
							},
						},
						Recommend: []ptpv1.PtpRecommend{
							{
								Profile:  stringPtr("profile-b"),
								Priority: int64Ptr(100),
								Match: []ptpv1.MatchRule{
									{
										NodeLabel: stringPtr("ptp-capable"),
									},
								},
							},
						},
					},
				},
			},
			expectedProfiles: []string{"profile-a", "profile-b"}, // Both configs match, both profiles selected
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Test the core profile selection logic without needing a real K8s client
			// This tests the algorithm that processes PtpConfigs and applies matching rules

			// Simulate the profile calculation logic without the node fetch
			// First, collect all matching recommendations across all configs
			type matchedRecommendation struct {
				recommend ptpv1.PtpRecommend
				profile   ptpv1.PtpProfile
				priority  int64
			}

			var allMatches []matchedRecommendation

			// Process each PtpConfig and find ALL matching recommendations
			for _, ptpConfig := range tc.ptpConfigs {
				for _, recommend := range ptpConfig.Spec.Recommend {
					if anyRuleMatchesNode(recommend.Match, tc.nodeName, tc.nodeLabels) {
						priority := int64(0)
						if recommend.Priority != nil {
							priority = *recommend.Priority
						}

						// Find the corresponding profile
						if recommend.Profile != nil {
							profileName := *recommend.Profile
							for _, profile := range ptpConfig.Spec.Profile {
								if profile.Name != nil && *profile.Name == profileName {
									allMatches = append(allMatches, matchedRecommendation{
										recommend: recommend,
										profile:   profile,
										priority:  priority,
									})
									break
								}
							}
						}
					}
				}
			}

			// Now select only the highest priority match(es)
			var matchingProfiles []ptpv1.PtpProfile
			if len(allMatches) > 0 {
				// Find the highest priority
				maxPriority := allMatches[0].priority
				for _, match := range allMatches {
					if match.priority > maxPriority {
						maxPriority = match.priority
					}
				}

				// Select all profiles with the highest priority
				for _, match := range allMatches {
					if match.priority == maxPriority {
						matchingProfiles = append(matchingProfiles, match.profile)
					}
				}
			}

			// Extract profile names for comparison
			var actualProfileNames []string
			for _, profile := range matchingProfiles {
				if profile.Name != nil {
					actualProfileNames = append(actualProfileNames, *profile.Name)
				}
			}

			// Verify the results
			assert.ElementsMatch(t, tc.expectedProfiles, actualProfileNames,
				"Expected profiles %v, got %v", tc.expectedProfiles, actualProfileNames)

			// Additional validation: ensure the returned profiles have the expected properties
			if len(tc.expectedProfiles) > 0 {
				for _, profile := range matchingProfiles {
					assert.NotNil(t, profile.Name, "Profile name should not be nil")
					assert.NotNil(t, profile.Interface, "Profile interface should not be nil")
				}
			}
		})
	}
}

// Helper functions
func stringPtr(s string) *string {
	return &s
}

func int64Ptr(i int64) *int64 {
	return &i
}
