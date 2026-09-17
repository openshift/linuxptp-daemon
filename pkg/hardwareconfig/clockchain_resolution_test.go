package hardwareconfig

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
)

// mockLeadingInterfaceResolver implements LeadingInterfaceResolver for testing
type mockLeadingInterfaceResolver struct {
	phcIDs     map[string]string        // iface -> PHC ID
	symlinks   map[string]string        // path -> target
	dirEntries map[string][]os.DirEntry // path -> entries
}

func newMockLeadingInterfaceResolver() *mockLeadingInterfaceResolver {
	return &mockLeadingInterfaceResolver{
		phcIDs:     make(map[string]string),
		symlinks:   make(map[string]string),
		dirEntries: make(map[string][]os.DirEntry),
	}
}

func (m *mockLeadingInterfaceResolver) GetPhcID(iface string) string {
	if phcID, ok := m.phcIDs[iface]; ok {
		return phcID
	}
	return ""
}

func (m *mockLeadingInterfaceResolver) Readlink(path string) (string, error) {
	if target, ok := m.symlinks[path]; ok {
		return target, nil
	}
	return "", os.ErrNotExist
}

func (m *mockLeadingInterfaceResolver) ReadDir(path string) ([]os.DirEntry, error) {
	if entries, ok := m.dirEntries[path]; ok {
		return entries, nil
	}
	return nil, os.ErrNotExist
}

// mockDirEntry implements os.DirEntry for testing
type mockDirEntry struct {
	name  string
	isDir bool
}

func (m *mockDirEntry) Name() string               { return m.name }
func (m *mockDirEntry) IsDir() bool                { return m.isDir }
func (m *mockDirEntry) Type() os.FileMode          { return 0 }
func (m *mockDirEntry) Info() (os.FileInfo, error) { return nil, nil }

// loadPtpConfigFromFile loads a PtpConfig from a YAML file
func loadPtpConfigFromFile(filename string) (*ptpv1.PtpConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	// Register the PtpConfig types with the scheme
	_ = ptpv1.AddToScheme(scheme.Scheme)

	// Create a decoder
	decode := serializer.NewCodecFactory(scheme.Scheme).UniversalDeserializer().Decode

	// Decode the YAML
	obj, _, err := decode(data, nil, nil)
	if err != nil {
		return nil, err
	}

	ptpConfig, ok := obj.(*ptpv1.PtpConfig)
	if !ok {
		return nil, err
	}

	return ptpConfig, nil
}

// TestClockChainResolution tests that a minimal hardwareconfig with clockType
// gets resolved with structure and behavior derived from ptpconfig and templates.
// It covers multiple hardware vendors (intel/e825, dell/XR8720t).
func TestClockChainResolution(t *testing.T) {
	testCases := []struct {
		name              string
		hwConfigFile      string
		hwDefPath         string
		expectedPtpInput  string
		expectedGnssInput string
	}{
		{
			name:              HwDefIntelE825,
			hwConfigFile:      "testdata/gnrd-hwconfig-minimal.yaml",
			hwDefPath:         HwDefIntelE825,
			expectedPtpInput:  "GNR-D_SDP0",
			expectedGnssInput: "GNSS_1PPS_IN",
		},
		{
			name:              "dell/XR8720t",
			hwConfigFile:      "testdata/gnrd-hwconfig-minimal-perla4.yaml",
			hwDefPath:         "dell/XR8720t",
			expectedPtpInput:  testPackageLabelREF0N,
			expectedGnssInput: testPackageLabelREF4P,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Load minimal hardwareconfig
			hwConfig, err := loadHardwareConfigFromFile(tc.hwConfigFile)
			assert.NoError(t, err)
			assert.NotNil(t, hwConfig)
			if hwConfig == nil {
				t.Fatal("hwConfig is nil")
			}

			// Verify it's minimal (no DPLL/Ethernet in structure)
			assert.NotNil(t, hwConfig.Spec.Profile.ClockType)
			assert.Equal(t, "T-BC", *hwConfig.Spec.Profile.ClockType)
			assert.NotNil(t, hwConfig.Spec.Profile.ClockChain)
			assert.Len(t, hwConfig.Spec.Profile.ClockChain.Structure, 1)

			subsystem := hwConfig.Spec.Profile.ClockChain.Structure[0]
			assert.Equal(t, testSubsystemLeader, subsystem.Name)
			assert.Equal(t, tc.hwDefPath, subsystem.HardwareSpecificDefinitions)

			// Before resolution: DPLL and Ethernet should be empty/omitted
			assert.Empty(t, subsystem.DPLL.NetworkInterface)
			assert.Empty(t, subsystem.DPLL.PhaseInputs)
			assert.Empty(t, subsystem.Ethernet)
			assert.Nil(t, hwConfig.Spec.Profile.ClockChain.Behavior)

			// Load ptpconfig
			ptpConfig, err := loadPtpConfigFromFile("testdata/tbc-gnrd.yaml")
			assert.NoError(t, err)
			assert.NotNil(t, ptpConfig)
			if ptpConfig == nil {
				t.Fatal("ptpConfig is nil")
			}

			// Find the matching profile
			var ptpProfile *ptpv1.PtpProfile
			for i := range ptpConfig.Spec.Profile {
				if ptpConfig.Spec.Profile[i].Name != nil &&
					ProfileNamesMatch(*ptpConfig.Spec.Profile[i].Name, hwConfig.Spec.RelatedPtpProfileName) {
					ptpProfile = &ptpConfig.Spec.Profile[i]
					break
				}
			}
			assert.NotNil(t, ptpProfile, "PTP profile %s not found", hwConfig.Spec.RelatedPtpProfileName)
			if ptpProfile == nil {
				t.Fatalf("PTP profile %s not found", hwConfig.Spec.RelatedPtpProfileName)
			}

			// Extract upstream ports from ptpconfig
			upstreamPorts := extractUpstreamPortsFromPtpProfile(ptpProfile)
			assert.NotEmpty(t, upstreamPorts, "Should find at least one upstream port")
			if len(upstreamPorts) == 0 {
				t.Fatal("Should find at least one upstream port")
			}
			assert.Contains(t, upstreamPorts, "eno2", "Should find eno2 as upstream port")

			// Set up fake ConfigMap loader for tests (reused later for hcm)
			fakeClient := fake.NewClientset()
			loader := NewBoardLabelMapLoader(fakeClient, "default")

			// Load behavior profile template
			behaviorTemplate, err := LoadBehaviorProfile(tc.hwDefPath, *hwConfig.Spec.Profile.ClockType, loader)
			assert.NoError(t, err)
			assert.NotNil(t, behaviorTemplate, "Behavior template should be loaded")
			if behaviorTemplate == nil {
				t.Fatal("Behavior template should be loaded")
			}

			// Verify template has pinRoles
			assert.NotEmpty(t, behaviorTemplate.PinRoles)
			assert.Equal(t, tc.expectedPtpInput, behaviorTemplate.PinRoles["ptpInputPin"])
			assert.Equal(t, tc.expectedGnssInput, behaviorTemplate.PinRoles["gnssInputPin"])

			// Set up mock leading interface resolver
			mockResolver := newMockLeadingInterfaceResolver()
			// Mock: eno2 -> PHC 0 -> PCI 0000:13:00.0 -> eno5
			mockResolver.phcIDs["eno2"] = testDevPtp0
			mockResolver.symlinks["/sys/class/ptp/ptp0/device"] = "../../../0000:13:00.0"
			mockResolver.dirEntries["/sys/bus/pci/devices/0000:13:00.0/net"] = []os.DirEntry{
				&mockDirEntry{name: "eno5", isDir: false},
			}

			// Inject mock resolver
			SetLeadingInterfaceResolver(mockResolver)
			defer ResetLeadingInterfaceResolver()

			// Resolve clock chain (this is what we're testing)
			hcm := NewHardwareConfigManager(fakeClient, "default", nil)
			resolvedConfig, err := hcm.ResolveClockChain(hwConfig, ptpConfig)
			assert.NoError(t, err)
			assert.NotNil(t, resolvedConfig)
			if resolvedConfig == nil {
				t.Fatal("resolvedConfig is nil")
			}

			// Verify structure was derived
			resolvedSubsystem := resolvedConfig.Spec.Profile.ClockChain.Structure[0]

			// NetworkInterface should be derived (leading interface found via PHC -> PCI -> net)
			assert.NotEmpty(t, resolvedSubsystem.DPLL.NetworkInterface)
			assert.Equal(t, "eno5", resolvedSubsystem.DPLL.NetworkInterface,
				"NetworkInterface should be derived from upstream port eno2 via PHC -> PCI -> net path")

			// PhaseInputs should be derived from pinRoles (ptpInputPin)
			assert.NotEmpty(t, resolvedSubsystem.DPLL.PhaseInputs)
			ptpInputPin, exists := resolvedSubsystem.DPLL.PhaseInputs[tc.expectedPtpInput]
			assert.True(t, exists, "PhaseInputs should contain ptpInputPin %s from template", tc.expectedPtpInput)
			assert.NotNil(t, ptpInputPin.Frequency)
			assert.Equal(t, int64(1), *ptpInputPin.Frequency, "PTP input should be 1 PPS")

			// Ethernet ports should be derived (all upstream ports)
			assert.NotEmpty(t, resolvedSubsystem.Ethernet)
			assert.Len(t, resolvedSubsystem.Ethernet, 1)
			assert.Equal(t, upstreamPorts, resolvedSubsystem.Ethernet[0].Ports,
				"Ethernet ports should match upstream ports")

			// Verify behavior derivation
			assert.NotNil(t, resolvedConfig.Spec.Profile.ClockChain.Behavior)
			behavior := resolvedConfig.Spec.Profile.ClockChain.Behavior

			// Sources should be instantiated with resolved variables
			assert.NotEmpty(t, behavior.Sources)
			ptpSource := findSourceByName(behavior.Sources, testSourcePTP)
			assert.NotNil(t, ptpSource, "PTP source should be present")
			if ptpSource == nil {
				t.Fatal("PTP source should be present")
			}
			assert.Equal(t, ptpv2alpha1.SourceTypePTP, ptpSource.SourceType)
			assert.Equal(t, testSubsystemLeader, ptpSource.Subsystem, "Subsystem should be resolved")
			assert.Equal(t, tc.expectedPtpInput, ptpSource.BoardLabel, "BoardLabel should be resolved from pinRoles")
			assert.Equal(t, upstreamPorts, ptpSource.PTPTimeReceivers,
				"PTPTimeReceivers should match upstream ports")

			// Conditions should be instantiated with resolved variables
			assert.NotEmpty(t, behavior.Conditions)
			initCondition := findConditionByName(behavior.Conditions, "Initialize T-BC")
			assert.NotNil(t, initCondition, "Initialize T-BC condition should be present")
			if initCondition == nil {
				t.Fatal("Initialize T-BC condition should be present")
			}
			// Init condition should use GNSS input pin for DPLL
			if len(initCondition.DesiredStates) > 0 && initCondition.DesiredStates[0].DPLL != nil {
				assert.Equal(t, tc.expectedGnssInput, initCondition.DesiredStates[0].DPLL.BoardLabel,
					"Init condition DPLL should use gnssInputPin from template")
			}

			lockedCondition := findConditionByName(behavior.Conditions, "PTP Source Locked")
			assert.NotNil(t, lockedCondition, "PTP Source Locked condition should be present")
			if lockedCondition == nil {
				t.Fatal("PTP Source Locked condition should be present")
			}
			// Locked condition should use PTP input pin for DPLL
			if len(lockedCondition.DesiredStates) > 0 && lockedCondition.DesiredStates[0].DPLL != nil {
				assert.Equal(t, tc.expectedPtpInput, lockedCondition.DesiredStates[0].DPLL.BoardLabel,
					"Locked condition DPLL should use ptpInputPin from template")
			}

			lostCondition := findConditionByName(behavior.Conditions, "PTP Source Lost - Leader Holdover")
			assert.NotNil(t, lostCondition, "PTP Source Lost condition should be present")
			if lostCondition == nil {
				t.Fatal("PTP Source Lost condition should be present")
			}

			// Verify template variables were resolved in conditions
			for _, condition := range behavior.Conditions {
				for _, desiredState := range condition.DesiredStates {
					if desiredState.DPLL != nil {
						assert.NotEqual(t, "{subsystem}", desiredState.DPLL.Subsystem,
							"Subsystem variable should be resolved")
						assert.NotEqual(t, "{ptpInputPin}", desiredState.DPLL.BoardLabel,
							"ptpInputPin variable should be resolved")
					}
				}
			}
		})
	}
}

// TestClockChainResolution_TGM verifies that a T-GM HardwareConfig skips
// upstream port derivation (no PTP time receivers), PhaseInputs derivation,
// and Ethernet derivation — none of which apply for grandmaster operation.
func TestClockChainResolution_TGM(t *testing.T) {
	hwConfig, err := loadHardwareConfigFromFile("testdata/gnrd-hwconfig-minimal-tgm.yaml")
	require.NoError(t, err)
	require.NotNil(t, hwConfig)

	assert.Equal(t, ClockTypeTGM, *hwConfig.Spec.Profile.ClockType)
	subsystem := hwConfig.Spec.Profile.ClockChain.Structure[0]
	assert.Equal(t, testSubsystemLeader, subsystem.Name)
	assert.Equal(t, HwDefIntelE825, subsystem.HardwareSpecificDefinitions)

	// T-GM provides networkInterface directly (no upstream port derivation)
	assert.Equal(t, testIfaceEns7f0, subsystem.DPLL.NetworkInterface)

	ptpConfig, err := loadPtpConfigFromFile("testdata/tgm-gnrd.yaml")
	require.NoError(t, err)
	require.NotNil(t, ptpConfig)

	// T-GM ptpconfig should have no upstream ports (all masterOnly=1)
	var ptpProfile *ptpv1.PtpProfile
	for i := range ptpConfig.Spec.Profile {
		if ptpConfig.Spec.Profile[i].Name != nil &&
			ProfileNamesMatch(*ptpConfig.Spec.Profile[i].Name, hwConfig.Spec.RelatedPtpProfileName) {
			ptpProfile = &ptpConfig.Spec.Profile[i]
			break
		}
	}
	require.NotNil(t, ptpProfile)
	upstreamPorts := extractUpstreamPortsFromPtpProfile(ptpProfile)
	assert.Empty(t, upstreamPorts, "T-GM should have no upstream ports")

	fakeClient := fake.NewClientset()
	hcm := NewHardwareConfigManager(fakeClient, "default", nil)
	resolvedConfig, err := hcm.ResolveClockChain(hwConfig, ptpConfig)
	require.NoError(t, err)
	require.NotNil(t, resolvedConfig)

	resolved := resolvedConfig.Spec.Profile.ClockChain.Structure[0]

	// NetworkInterface should be preserved (not derived from upstream)
	assert.Equal(t, testIfaceEns7f0, resolved.DPLL.NetworkInterface)

	// T-GM should NOT derive PTP-related PhaseInputs or Ethernet from upstream ports.
	// (PhaseInputs may still contain pins from delay compensation injection.)
	if len(resolved.DPLL.PhaseInputs) > 0 {
		_, hasPtpInput := resolved.DPLL.PhaseInputs["GNR-D_SDP0"]
		assert.False(t, hasPtpInput, "T-GM should not derive PTP input pin GNR-D_SDP0")
	}
	assert.Empty(t, resolved.Ethernet, "T-GM should not derive Ethernet ports")

	// Behavior should still be derived from template
	assert.NotNil(t, resolvedConfig.Spec.Profile.ClockChain.Behavior)
	behavior := resolvedConfig.Spec.Profile.ClockChain.Behavior
	assert.NotEmpty(t, behavior.Sources)

	// Should have GNSS source (not PTP)
	var gnssSource *ptpv2alpha1.SourceConfig
	for i, s := range behavior.Sources {
		if s.SourceType == ptpv2alpha1.SourceTypeGNSS {
			gnssSource = &behavior.Sources[i]
			break
		}
	}
	assert.NotNil(t, gnssSource, "T-GM should have GNSS source")
	if gnssSource != nil {
		assert.Equal(t, "GNSS_1PPS_IN", gnssSource.BoardLabel)
	}

	// Should have Initialize T-GM condition
	initCondition := findConditionByName(behavior.Conditions, "Initialize T-GM")
	assert.NotNil(t, initCondition, "T-GM should have Initialize T-GM condition")
}

// TestClockChainResolution_TGM_DeriveNetworkInterface verifies that a T-GM
// config without networkInterface auto-derives it from any interface in the
// PTP config, using the same leading-interface resolution as T-BC.
// TestClockChainResolution_TGM_DeriveNetworkInterface verifies that a T-GM
// config without networkInterface auto-derives it from any interface in the
// PTP config, using the same leading-interface resolution as T-BC.
func TestClockChainResolution_TGM_DeriveNetworkInterface(t *testing.T) {
	hwConfig, err := loadHardwareConfigFromFile("testdata/gnrd-hwconfig-minimal-tgm.yaml")
	require.NoError(t, err)

	// Remove networkInterface to trigger auto-derive
	hwConfig.Spec.Profile.ClockChain.Structure[0].DPLL.NetworkInterface = ""

	ptpConfig, err := loadPtpConfigFromFile("testdata/tgm-gnrd.yaml")
	require.NoError(t, err)

	// T-GM ptpconfig has ens7f0 with masterOnly=1; auto-derive should use it
	allIfaces := extractInterfacesFromPtpProfile(&ptpConfig.Spec.Profile[0])
	require.NotEmpty(t, allIfaces, "PTP config should have at least one interface")
	assert.Equal(t, testIfaceEns7f0, allIfaces[0])

	// Mock leading interface resolver: ens7f0 -> ptp0 -> PCI -> ens7f0
	mockResolver := newMockLeadingInterfaceResolver()
	mockResolver.phcIDs[testIfaceEns7f0] = testDevPtp0
	mockResolver.symlinks["/sys/class/ptp/ptp0/device"] = "../../../0000:51:00.0"
	mockResolver.dirEntries["/sys/bus/pci/devices/0000:51:00.0/net"] = []os.DirEntry{
		&mockDirEntry{name: testIfaceEns7f0, isDir: false},
	}
	SetLeadingInterfaceResolver(mockResolver)
	defer ResetLeadingInterfaceResolver()

	fakeClient := fake.NewClientset()
	hcm := NewHardwareConfigManager(fakeClient, "default", nil)
	resolved, err := hcm.ResolveClockChain(hwConfig, ptpConfig)
	require.NoError(t, err)

	assert.Equal(t, testIfaceEns7f0, resolved.Spec.Profile.ClockChain.Structure[0].DPLL.NetworkInterface,
		"NetworkInterface should be auto-derived from T-GM PTP config interface")
}

// TestClockChainResolution_TGM_NoInterfaces verifies that a T-GM config
// with no interfaces in the PTP config returns a clear error.
func TestClockChainResolution_TGM_NoInterfaces(t *testing.T) {
	hwConfig, err := loadHardwareConfigFromFile("testdata/gnrd-hwconfig-minimal-tgm.yaml")
	require.NoError(t, err)

	hwConfig.Spec.Profile.ClockChain.Structure[0].DPLL.NetworkInterface = ""

	// Create a PTP config with no interface sections (only [global])
	emptyConf := "[global]\nmasterOnly 1\n"
	ptpConfig, err := loadPtpConfigFromFile("testdata/tgm-gnrd.yaml")
	require.NoError(t, err)
	ptpConfig.Spec.Profile[0].Ptp4lConf = &emptyConf

	fakeClient := fake.NewClientset()
	hcm := NewHardwareConfigManager(fakeClient, "default", nil)
	_, err = hcm.ResolveClockChain(hwConfig, ptpConfig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no interfaces found in ptpconfig for T-GM subsystem")
}

// TestClockChainResolution_DualUpstream verifies that a minimal HardwareConfig
// paired with a PTP config containing two masterOnly=0 ports correctly derives
// both upstream ports into PTPTimeReceivers and Ethernet.
func TestClockChainResolution_DualUpstream(t *testing.T) {
	hwConfig, err := loadHardwareConfigFromFile("testdata/gnrd-hwconfig-minimal-perla4.yaml")
	assert.NoError(t, err)
	if !assert.NotNil(t, hwConfig) {
		t.Fatal("hwConfig is nil")
	}

	// Verify minimal — no behavior, no ethernet, no network interface
	assert.Nil(t, hwConfig.Spec.Profile.ClockChain.Behavior)
	subsystem := hwConfig.Spec.Profile.ClockChain.Structure[0]
	assert.Empty(t, subsystem.DPLL.NetworkInterface)
	assert.Empty(t, subsystem.Ethernet)

	// Load dual-upstream PTP config
	ptpConfig, err := loadPtpConfigFromFile("testdata/tbc-gnrd-dual-upstream.yaml")
	assert.NoError(t, err)
	if !assert.NotNil(t, ptpConfig) {
		t.Fatal("ptpConfig is nil")
	}

	// Find the TR profile and verify it has two upstream ports
	var ptpProfile *ptpv1.PtpProfile
	for i := range ptpConfig.Spec.Profile {
		if ptpConfig.Spec.Profile[i].Name != nil &&
			ProfileNamesMatch(*ptpConfig.Spec.Profile[i].Name, hwConfig.Spec.RelatedPtpProfileName) {
			ptpProfile = &ptpConfig.Spec.Profile[i]
			break
		}
	}
	if !assert.NotNil(t, ptpProfile, "profile %s not found", hwConfig.Spec.RelatedPtpProfileName) {
		t.Fatal()
	}

	upstreamPorts := extractUpstreamPortsFromPtpProfile(ptpProfile)
	assert.Len(t, upstreamPorts, 2, "Should find exactly two upstream ports")
	assert.Contains(t, upstreamPorts, "eno8703")
	assert.Contains(t, upstreamPorts, "eno8903")

	// Set up mock leading interface resolver:
	// eno8703 -> PHC 0 -> PCI 0000:87:00.0 -> eno8703 (leading interface)
	mockResolver := newMockLeadingInterfaceResolver()
	mockResolver.phcIDs["eno8703"] = testDevPtp0
	mockResolver.symlinks["/sys/class/ptp/ptp0/device"] = "../../../0000:87:00.0"
	mockResolver.dirEntries["/sys/bus/pci/devices/0000:87:00.0/net"] = []os.DirEntry{
		&mockDirEntry{name: "eno8703", isDir: false},
	}
	SetLeadingInterfaceResolver(mockResolver)
	defer ResetLeadingInterfaceResolver()

	fakeClient := fake.NewClientset()
	hcm := NewHardwareConfigManager(fakeClient, "default", nil)
	resolvedConfig, err := hcm.ResolveClockChain(hwConfig, ptpConfig)
	assert.NoError(t, err)
	if !assert.NotNil(t, resolvedConfig) {
		t.Fatal("resolvedConfig is nil")
	}

	// --- Structure assertions ---
	resolved := resolvedConfig.Spec.Profile.ClockChain.Structure[0]

	assert.Equal(t, "eno8703", resolved.DPLL.NetworkInterface,
		"NetworkInterface should be derived from first upstream port")

	assert.NotEmpty(t, resolved.DPLL.PhaseInputs)
	ptpInputPin, exists := resolved.DPLL.PhaseInputs[testPackageLabelREF0N]
	assert.True(t, exists, "PhaseInputs should contain ptpInputPin from dell/XR8720t template")
	assert.NotNil(t, ptpInputPin.Frequency)
	assert.Equal(t, int64(1), *ptpInputPin.Frequency)

	assert.Len(t, resolved.Ethernet, 1)
	assert.Equal(t, upstreamPorts, resolved.Ethernet[0].Ports,
		"Ethernet ports should contain both upstream ports")

	// --- Behavior assertions ---
	behavior := resolvedConfig.Spec.Profile.ClockChain.Behavior
	if !assert.NotNil(t, behavior) {
		t.Fatal("behavior is nil")
	}

	// PTP source should have both ports as PTPTimeReceivers
	ptpSource := findSourceByName(behavior.Sources, testSourcePTP)
	if !assert.NotNil(t, ptpSource, "PTP source should exist") {
		t.Fatal()
	}
	assert.Equal(t, ptpv2alpha1.SourceTypePTP, ptpSource.SourceType)
	assert.Equal(t, testSubsystemLeader, ptpSource.Subsystem)
	assert.Equal(t, testPackageLabelREF0N, ptpSource.BoardLabel)
	assert.Equal(t, upstreamPorts, ptpSource.PTPTimeReceivers,
		"PTPTimeReceivers should contain both upstream ports derived from PTP config")

	// All three conditions should be present
	initCond := findConditionByName(behavior.Conditions, "Initialize T-BC")
	assert.NotNil(t, initCond, "Initialize T-BC condition should be present")
	if initCond != nil && len(initCond.DesiredStates) > 0 && initCond.DesiredStates[0].DPLL != nil {
		assert.Equal(t, testPackageLabelREF4P, initCond.DesiredStates[0].DPLL.BoardLabel)
		assert.Equal(t, testSubsystemLeader, initCond.DesiredStates[0].DPLL.Subsystem)
	}

	lockedCond := findConditionByName(behavior.Conditions, "PTP Source Locked")
	assert.NotNil(t, lockedCond, "PTP Source Locked condition should be present")

	lostCond := findConditionByName(behavior.Conditions, "PTP Source Lost - Leader Holdover")
	assert.NotNil(t, lostCond, "PTP Source Lost condition should be present")

	// No unresolved template variables should remain
	for _, condition := range behavior.Conditions {
		for _, ds := range condition.DesiredStates {
			if ds.DPLL != nil {
				assert.NotContains(t, ds.DPLL.Subsystem, "{", "template variable should be resolved")
				assert.NotContains(t, ds.DPLL.BoardLabel, "{", "template variable should be resolved")
			}
		}
	}
}

// Helper functions

func findSourceByName(sources []ptpv2alpha1.SourceConfig, name string) *ptpv2alpha1.SourceConfig {
	for i := range sources {
		if sources[i].Name == name {
			return &sources[i]
		}
	}
	return nil
}

func findConditionByName(conditions []ptpv2alpha1.Condition, name string) *ptpv2alpha1.Condition {
	for i := range conditions {
		if conditions[i].Name == name {
			return &conditions[i]
		}
	}
	return nil
}

func TestProfileNamesMatch(t *testing.T) {
	tests := []struct {
		stored    string
		requested string
		want      bool
	}{
		// Unqualified stored name without prefix does not match (no exact-match branch).
		{"01-tbc-tr", "01-tbc-tr", false},
		// PtpConfig resource name carried as prefix (Kubernetes CR names have no underscores,
		// so the first "_" is always the separator).
		{"t-bc_01-tbc-tr", "01-tbc-tr", true},
		{"my-ptpconfig_01-tbc-tr", "01-tbc-tr", true},
		// Wrong profile name.
		{"t-bc_01-tbc-tr", "00-tbc-tt", false},
		// Stored has no prefix and names differ.
		{"other-profile", "01-tbc-tr", false},
		// Prefix shares characters with profile but has no "_" separator.
		{"x01-tbc-tr", "01-tbc-tr", false},

		// Profile name itself contains underscores — only the first "_" is the separator,
		// so "foo_bar" is the full profile name, not a false sub-match of "bar".
		{"prefix_foo_bar", "foo_bar", true},
		{"prefix_foo_bar", "bar", false},

		// False-positive regression: old HasSuffix would match "a_b_c" against "c".
		{"a_b_c", "c", false},

		// Empty / whitespace requested is always rejected.
		{"", "", false},
		{"t-bc_", "", false},
		{"t-bc_profile", "  ", false},
		{"", "something", false},

		// Stored is empty, requested is non-empty.
		{"", "profile", false},
	}

	for _, tc := range tests {
		got := ProfileNamesMatch(tc.stored, tc.requested)
		assert.Equal(t, tc.want, got, "ProfileNamesMatch(%q, %q)", tc.stored, tc.requested)
	}
}

// TestClockChainResolutionWithE830 verifies that ResolveClockChain succeeds for
// a combined E825+E830 config: the leader (E825) gets full structure/behavior
// derivation while E830 subsystems are skipped as unmanaged DPLLs.
func TestClockChainResolutionWithE830(t *testing.T) {
	hwConfig, err := loadHardwareConfigFromFile("testdata/gnrd-hwconfig-e830.yaml")
	require.NoError(t, err)
	require.NotNil(t, hwConfig, "hwConfig must not be nil")

	assert.Equal(t, "T-BC", *hwConfig.Spec.Profile.ClockType)
	assert.GreaterOrEqual(t, len(hwConfig.Spec.Profile.ClockChain.Structure), 2)

	// Capture original E830 subsystem state before resolution
	type e830Snapshot struct {
		name             string
		networkInterface string
	}
	var e830Before []e830Snapshot
	for _, sub := range hwConfig.Spec.Profile.ClockChain.Structure[1:] {
		e830Before = append(e830Before, e830Snapshot{
			name:             sub.Name,
			networkInterface: sub.DPLL.NetworkInterface,
		})
	}

	// Load ptpconfig
	ptpConfig, err := loadPtpConfigFromFile("testdata/tbc-gnrd.yaml")
	assert.NoError(t, err)
	if ptpConfig == nil {
		t.Fatal("ptpConfig is nil")
	}

	// Set up mock leading interface resolver for the leader subsystem
	mockResolver := newMockLeadingInterfaceResolver()
	mockResolver.phcIDs["eno2"] = testDevPtp0
	mockResolver.symlinks["/sys/class/ptp/ptp0/device"] = "../../../0000:13:00.0"
	mockResolver.dirEntries["/sys/bus/pci/devices/0000:13:00.0/net"] = []os.DirEntry{
		&mockDirEntry{name: "eno5", isDir: false},
	}
	SetLeadingInterfaceResolver(mockResolver)
	defer ResetLeadingInterfaceResolver()

	fakeClient := fake.NewClientset()
	hcm := NewHardwareConfigManager(fakeClient, "default", nil)

	resolvedConfig, err := hcm.ResolveClockChain(hwConfig, ptpConfig)
	require.NoError(t, err, "ResolveClockChain should succeed with E830 subsystems")
	require.NotNil(t, resolvedConfig, "resolvedConfig must not be nil")

	// Verify leader (E825) was resolved normally
	leader := resolvedConfig.Spec.Profile.ClockChain.Structure[0]
	assert.Equal(t, "leader", leader.Name)
	assert.Equal(t, "dell/XR8720t", leader.HardwareSpecificDefinitions)
	assert.Equal(t, "eno5", leader.DPLL.NetworkInterface,
		"Leader NetworkInterface should be derived")
	assert.NotEmpty(t, leader.DPLL.PhaseInputs,
		"Leader should have derived PhaseInputs")
	assert.NotEmpty(t, leader.Ethernet,
		"Leader should have derived Ethernet ports")

	// Verify behavior was derived (only for the leader, E830 skipped)
	assert.NotNil(t, resolvedConfig.Spec.Profile.ClockChain.Behavior)
	ptpSource := findSourceByName(resolvedConfig.Spec.Profile.ClockChain.Behavior.Sources, "PTP")
	assert.NotNil(t, ptpSource, "PTP source should be present from leader behavior")

	// Verify E830 subsystems were left untouched
	for i, snap := range e830Before {
		sub := resolvedConfig.Spec.Profile.ClockChain.Structure[1+i]
		assert.Equal(t, snap.name, sub.Name)
		assert.Equal(t, "intel/e830", sub.HardwareSpecificDefinitions)
		assert.Equal(t, snap.networkInterface, sub.DPLL.NetworkInterface,
			"E830 subsystem %s networkInterface should be preserved", sub.Name)
		assert.Empty(t, sub.DPLL.PhaseInputs,
			"E830 subsystem %s should have no PhaseInputs", sub.Name)
		assert.Empty(t, sub.DPLL.PhaseOutputs,
			"E830 subsystem %s should have no PhaseOutputs", sub.Name)
		assert.Empty(t, sub.Ethernet,
			"E830 subsystem %s should have no Ethernet ports", sub.Name)
	}

	t.Logf("ResolveClockChain succeeded: leader derived, %d E830 subsystems skipped", len(e830Before))
}

func TestMergeSourceConfig(t *testing.T) {
	t.Run("merges gnssConfig onto template", func(t *testing.T) {
		tpl := ptpv2alpha1.SourceConfig{
			Name:       testSourceGNSS,
			SourceType: ptpv2alpha1.SourceTypeGNSS,
			Subsystem:  testSubsystemLeader,
			BoardLabel: "GNSS_1PPS_IN",
		}
		user := ptpv2alpha1.SourceConfig{
			Name:       testSourceGNSS,
			SourceType: ptpv2alpha1.SourceTypeGNSS,
			GNSSConfig: &ptpv2alpha1.GNSSConfig{
				Init: ptpv2alpha1.GNSSInit{AntennaVoltage: true},
			},
		}
		mergeSourceConfig(&tpl, &user)

		assert.Equal(t, testSubsystemLeader, tpl.Subsystem, "template subsystem preserved")
		assert.Equal(t, "GNSS_1PPS_IN", tpl.BoardLabel, "template boardLabel preserved")
		assert.NotNil(t, tpl.GNSSConfig, "user gnssConfig merged")
		assert.True(t, tpl.GNSSConfig.Init.AntennaVoltage)
	})

	t.Run("user overrides subsystem and boardLabel", func(t *testing.T) {
		tpl := ptpv2alpha1.SourceConfig{
			Name:       testSourcePTP,
			Subsystem:  testSubsystemLeader,
			BoardLabel: "DEFAULT_PIN",
		}
		user := ptpv2alpha1.SourceConfig{
			Name:       testSourcePTP,
			Subsystem:  "custom-sub",
			BoardLabel: "CUSTOM_PIN",
		}
		mergeSourceConfig(&tpl, &user)

		assert.Equal(t, "custom-sub", tpl.Subsystem)
		assert.Equal(t, "CUSTOM_PIN", tpl.BoardLabel)
	})

	t.Run("empty user fields do not overwrite template", func(t *testing.T) {
		tpl := ptpv2alpha1.SourceConfig{
			Name:             testSourcePTP,
			Subsystem:        testSubsystemLeader,
			BoardLabel:       "TEMPLATE_PIN",
			PTPTimeReceivers: []string{testIfaceE810},
		}
		user := ptpv2alpha1.SourceConfig{
			Name: testSourcePTP,
			// All other fields empty/nil
		}
		mergeSourceConfig(&tpl, &user)

		assert.Equal(t, testSubsystemLeader, tpl.Subsystem)
		assert.Equal(t, "TEMPLATE_PIN", tpl.BoardLabel)
		assert.Equal(t, []string{testIfaceE810}, tpl.PTPTimeReceivers)
		assert.Nil(t, tpl.GNSSConfig)
	})

	t.Run("user ptpTimeReceivers override template", func(t *testing.T) {
		tpl := ptpv2alpha1.SourceConfig{
			Name:             testSourcePTP,
			PTPTimeReceivers: []string{"old-port"},
		}
		user := ptpv2alpha1.SourceConfig{
			Name:             testSourcePTP,
			PTPTimeReceivers: []string{"new-port1", "new-port2"},
		}
		mergeSourceConfig(&tpl, &user)

		assert.Equal(t, []string{"new-port1", "new-port2"}, tpl.PTPTimeReceivers)
	})
}

func TestDeriveBehavior_UnmatchedUserSourceAdded(t *testing.T) {
	fakeClient := fake.NewClientset()
	hcm := NewHardwareConfigManager(fakeClient, "default", nil)

	clockType := testClockTypeTGM
	hwConfig := &ptpv2alpha1.HardwareConfig{
		Spec: ptpv2alpha1.HardwareConfigSpec{
			Profile: ptpv2alpha1.HardwareProfile{
				ClockType: &clockType,
				ClockChain: &ptpv2alpha1.ClockChain{
					Structure: []ptpv2alpha1.Subsystem{
						{
							Name:                        testSubsystemLeader,
							HardwareSpecificDefinitions: HwDefIntelE825,
							DPLL: ptpv2alpha1.DPLL{
								NetworkInterface: testIfaceEns7f0,
							},
							Ethernet: []ptpv2alpha1.Ethernet{
								{Ports: []string{testIfaceEns7f0}},
							},
						},
					},
					Behavior: &ptpv2alpha1.Behavior{
						Sources: []ptpv2alpha1.SourceConfig{
							{
								Name:       "CustomSource",
								SourceType: ptpv2alpha1.SourceTypeDPLL,
								Subsystem:  testSubsystemLeader,
								BoardLabel: "CUSTOM_PIN",
							},
						},
					},
				},
			},
		},
	}

	err := hcm.deriveBehavior(hwConfig, clockType)
	assert.NoError(t, err)

	behavior := hwConfig.Spec.Profile.ClockChain.Behavior
	assert.NotNil(t, behavior)

	// Should have the template GNSS source + the unmatched custom source
	var foundGNSS, foundCustom bool
	for _, s := range behavior.Sources {
		if s.Name == testSourceGNSS {
			foundGNSS = true
		}
		if s.Name == "CustomSource" {
			foundCustom = true
			assert.Equal(t, ptpv2alpha1.SourceTypeDPLL, s.SourceType)
			assert.Equal(t, "CUSTOM_PIN", s.BoardLabel)
		}
	}
	assert.True(t, foundGNSS, "template GNSS source should be present")
	assert.True(t, foundCustom, "unmatched user source should be added")
}

func TestDeriveBehavior_UserConditionsPreserved(t *testing.T) {
	fakeClient := fake.NewClientset()
	hcm := NewHardwareConfigManager(fakeClient, "default", nil)

	clockType := testClockTypeTGM
	hwConfig := &ptpv2alpha1.HardwareConfig{
		Spec: ptpv2alpha1.HardwareConfigSpec{
			Profile: ptpv2alpha1.HardwareProfile{
				ClockType: &clockType,
				ClockChain: &ptpv2alpha1.ClockChain{
					Structure: []ptpv2alpha1.Subsystem{
						{
							Name:                        testSubsystemLeader,
							HardwareSpecificDefinitions: HwDefIntelE825,
							DPLL: ptpv2alpha1.DPLL{
								NetworkInterface: testIfaceEns7f0,
							},
							Ethernet: []ptpv2alpha1.Ethernet{
								{Ports: []string{testIfaceEns7f0}},
							},
						},
					},
					Behavior: &ptpv2alpha1.Behavior{
						// User overrides "Initialize T-GM" with custom desired states
						Conditions: []ptpv2alpha1.Condition{
							{
								Name: "Initialize T-GM",
								DesiredStates: []ptpv2alpha1.DesiredState{
									{DPLL: &ptpv2alpha1.DPLLDesiredState{
										Subsystem:  testSubsystemLeader,
										BoardLabel: "USER_OVERRIDE_PIN",
									}},
								},
							},
							{
								Name: "Custom User Condition",
								DesiredStates: []ptpv2alpha1.DesiredState{
									{DPLL: &ptpv2alpha1.DPLLDesiredState{
										Subsystem:  testSubsystemLeader,
										BoardLabel: "USER_PIN",
									}},
								},
							},
						},
					},
				},
			},
		},
	}

	err := hcm.deriveBehavior(hwConfig, clockType)
	assert.NoError(t, err)

	behavior := hwConfig.Spec.Profile.ClockChain.Behavior
	require.NotNil(t, behavior)

	// "Initialize T-GM" should be the user's version (overrides template)
	initCond := findConditionByName(behavior.Conditions, "Initialize T-GM")
	require.NotNil(t, initCond, "Initialize T-GM should be present")
	require.Len(t, initCond.DesiredStates, 1)
	assert.Equal(t, "USER_OVERRIDE_PIN", initCond.DesiredStates[0].DPLL.BoardLabel,
		"User condition should override template condition")

	// "Custom User Condition" should be preserved (unmatched, appended)
	customCond := findConditionByName(behavior.Conditions, "Custom User Condition")
	require.NotNil(t, customCond, "Unmatched user condition should be appended")
	assert.Equal(t, "USER_PIN", customCond.DesiredStates[0].DPLL.BoardLabel)

	// Template sources should still be present
	assert.NotEmpty(t, behavior.Sources, "Template sources should be derived")
}

func Test_mergeSourceConfig(t *testing.T) {
	const ttyTpl = "/dev/tty-tpl"
	const ttyUser = "/dev/tty-user"
	const testCustomCommand = "custom-command"
	const testStandardCommand = "standard-command"

	tests := []struct {
		name     string
		tpl      *ptpv2alpha1.SourceConfig
		user     *ptpv2alpha1.SourceConfig
		expected *ptpv2alpha1.SourceConfig
	}{
		{
			name:     "all empty",
			tpl:      &ptpv2alpha1.SourceConfig{},
			user:     &ptpv2alpha1.SourceConfig{},
			expected: &ptpv2alpha1.SourceConfig{},
		},
		{
			name: "update simple fields",
			tpl: &ptpv2alpha1.SourceConfig{
				Subsystem:  "tpl-sub",
				BoardLabel: "tpl-board",
			},
			user: &ptpv2alpha1.SourceConfig{
				Subsystem:        "user-sub",
				BoardLabel:       "user-board",
				PTPTimeReceivers: []string{"user-ptp"},
			},
			expected: &ptpv2alpha1.SourceConfig{
				Subsystem:        "user-sub",
				BoardLabel:       "user-board",
				PTPTimeReceivers: []string{"user-ptp"},
			},
		},
		{
			name: "empty user fields do not override tpl fields",
			tpl: &ptpv2alpha1.SourceConfig{
				Subsystem:        "tpl-sub",
				BoardLabel:       "tpl-board",
				PTPTimeReceivers: []string{"tpl-ptp"},
			},
			user: &ptpv2alpha1.SourceConfig{
				Subsystem:        "",
				BoardLabel:       "",
				PTPTimeReceivers: []string{},
			},
			expected: &ptpv2alpha1.SourceConfig{
				Subsystem:        "tpl-sub",
				BoardLabel:       "tpl-board",
				PTPTimeReceivers: []string{"tpl-ptp"},
			},
		},
		{
			name: "user GNSS config nil, tpl GNSS config untouched",
			tpl: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Match: &ptpv2alpha1.GNSSMatcher{
						TTYDevice: ttyTpl,
					},
				},
			},
			user: &ptpv2alpha1.SourceConfig{},
			expected: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Match: &ptpv2alpha1.GNSSMatcher{
						TTYDevice: ttyTpl,
					},
				},
			},
		},
		{
			name: "user GNSS config set, no matches in either",
			tpl:  &ptpv2alpha1.SourceConfig{},
			user: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{},
			},
			expected: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{},
			},
		},
		{
			name: "user GNSS config set, user match set, tpl GNSS config nil",
			tpl:  &ptpv2alpha1.SourceConfig{},
			user: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Match: &ptpv2alpha1.GNSSMatcher{
						TTYDevice: ttyUser,
					},
				},
			},
			expected: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Match: &ptpv2alpha1.GNSSMatcher{
						TTYDevice: ttyUser,
					},
				},
			},
		},
		{
			name: "user GNSS config set with init, tpl match preserved",
			tpl: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Match: &ptpv2alpha1.GNSSMatcher{
						TTYDevice: ttyTpl,
					},
				},
			},
			user: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Init: ptpv2alpha1.GNSSInit{
						// Dummy field initialization, we just check if it gets copied
					},
				},
			},
			expected: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Init: ptpv2alpha1.GNSSInit{},
					Match: &ptpv2alpha1.GNSSMatcher{
						TTYDevice: ttyTpl,
					},
				},
			},
		},
		{
			name: "user GNSS config set, user match overrides tpl match",
			tpl: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Match: &ptpv2alpha1.GNSSMatcher{
						TTYDevice: ttyTpl,
					},
				},
			},
			user: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Match: &ptpv2alpha1.GNSSMatcher{
						TTYDevice: ttyUser,
					},
				},
			},
			expected: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Match: &ptpv2alpha1.GNSSMatcher{
						TTYDevice: ttyUser,
					},
				},
			},
		},
		{
			name: "user GNSS config set with no match, tpl GNSS config set with no match",
			tpl: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{},
			},
			user: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{},
			},
			expected: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{},
			},
		},
		{
			name: "user provides init value, already one in template",
			tpl: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Init: ptpv2alpha1.GNSSInit{
						ExtraCommands: []ptpv2alpha1.UBLXCommand{
							{Args: []string{testStandardCommand}, Record: true},
						},
					},
				},
			},
			user: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Init: ptpv2alpha1.GNSSInit{
						ExtraCommands: []ptpv2alpha1.UBLXCommand{
							{Args: []string{testCustomCommand}, Record: true},
						},
					},
				},
			},
			expected: &ptpv2alpha1.SourceConfig{
				GNSSConfig: &ptpv2alpha1.GNSSConfig{
					Init: ptpv2alpha1.GNSSInit{
						ExtraCommands: []ptpv2alpha1.UBLXCommand{
							{Args: []string{testCustomCommand}, Record: true},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mergeSourceConfig(tt.tpl, tt.user)
			assert.Equal(t, tt.expected, tt.tpl)

			// Verify shallow copy for GNSSConfig and Match if they were merged from user
			if tt.user.GNSSConfig != nil {
				// changing tpl should not affect user
				if tt.tpl.GNSSConfig.Match != nil {
					// We mutate the copied match string to see if the user's match is untouched
					originalTTY := tt.tpl.GNSSConfig.Match.TTYDevice
					tt.tpl.GNSSConfig.Match.TTYDevice = "/dev/mutated"

					if tt.user.GNSSConfig.Match != nil {
						assert.NotEqual(t, "/dev/mutated", tt.user.GNSSConfig.Match.TTYDevice, "user GNSSConfig.Match was not shallow-copied")
					}

					// Revert mutation
					tt.tpl.GNSSConfig.Match.TTYDevice = originalTTY
				}
			}
		})
	}
}
