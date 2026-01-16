package hardwareconfig

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
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
// gets resolved with structure and behavior derived from ptpconfig and templates
func TestClockChainResolution(t *testing.T) {
	// Load minimal hardwareconfig
	hwConfig, err := loadHardwareConfigFromFile("testdata/gnrd-hwconfig-minimal.yaml")
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
	assert.Equal(t, "leader", subsystem.Name)
	assert.Equal(t, "intel/e825", subsystem.HardwareSpecificDefinitions)

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
			*ptpConfig.Spec.Profile[i].Name == hwConfig.Spec.RelatedPtpProfileName {
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

	// Load behavior profile template
	behaviorTemplate, err := LoadBehaviorProfile("intel/e825", *hwConfig.Spec.Profile.ClockType)
	assert.NoError(t, err)
	assert.NotNil(t, behaviorTemplate, "Behavior template should be loaded")
	if behaviorTemplate == nil {
		t.Fatal("Behavior template should be loaded")
	}

	// Verify template has pinRoles
	assert.NotEmpty(t, behaviorTemplate.PinRoles)
	assert.Equal(t, "GNR-D_SDP0", behaviorTemplate.PinRoles["ptpInputPin"])
	assert.Equal(t, "GNSS_1PPS_IN", behaviorTemplate.PinRoles["gnssInputPin"])
	assert.Equal(t, "GNSS_1PPS_IN", behaviorTemplate.PinRoles["gnssInputPin"])

	// Set up mock leading interface resolver
	mockResolver := newMockLeadingInterfaceResolver()
	// Mock: eno2 -> PHC 0 -> PCI 0000:13:00.0 -> eno5
	mockResolver.phcIDs["eno2"] = "/dev/ptp0"
	mockResolver.symlinks["/sys/class/ptp/ptp0/device"] = "../../../0000:13:00.0"
	mockResolver.dirEntries["/sys/bus/pci/devices/0000:13:00.0/net"] = []os.DirEntry{
		&mockDirEntry{name: "eno5", isDir: false},
	}

	// Inject mock resolver
	SetLeadingInterfaceResolver(mockResolver)
	defer ResetLeadingInterfaceResolver()

	// Resolve clock chain (this is what we're testing)
	resolvedConfig, err := ResolveClockChain(hwConfig, ptpConfig)
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

	// PhaseInputs should be derived from pinRoles (ptpInputPin -> GNR-D_SDP0)
	assert.NotEmpty(t, resolvedSubsystem.DPLL.PhaseInputs)
	ptpInputPin, exists := resolvedSubsystem.DPLL.PhaseInputs["GNR-D_SDP0"]
	assert.True(t, exists, "PhaseInputs should contain ptpInputPin from template")
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
	ptpSource := findSourceByName(behavior.Sources, "PTP")
	assert.NotNil(t, ptpSource, "PTP source should be present")
	if ptpSource == nil {
		t.Fatal("PTP source should be present")
	}
	assert.Equal(t, "ptpTimeReceiver", ptpSource.SourceType)
	assert.Equal(t, "leader", ptpSource.Subsystem, "Subsystem should be resolved")
	assert.Equal(t, "GNR-D_SDP0", ptpSource.BoardLabel, "BoardLabel should be resolved from pinRoles")
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
		assert.Equal(t, "GNSS_1PPS_IN", initCondition.DesiredStates[0].DPLL.BoardLabel,
			"Init condition DPLL should use gnssInputPin from template")
	}

	lockedCondition := findConditionByName(behavior.Conditions, "PTP Source Locked")
	assert.NotNil(t, lockedCondition, "PTP Source Locked condition should be present")
	if lockedCondition == nil {
		t.Fatal("PTP Source Locked condition should be present")
	}
	// Locked condition should use PTP input pin for DPLL
	if len(lockedCondition.DesiredStates) > 0 && lockedCondition.DesiredStates[0].DPLL != nil {
		assert.Equal(t, "GNR-D_SDP0", lockedCondition.DesiredStates[0].DPLL.BoardLabel,
			"Locked condition DPLL should use ptpInputPin from template")
	}

	lostCondition := findConditionByName(behavior.Conditions, "PTP Source Lost - Leader Holdover")
	assert.NotNil(t, lostCondition, "PTP Source Lost condition should be present")
	if lostCondition == nil {
		t.Fatal("PTP Source Lost condition should be present")
	}

	// Verify template variables were resolved in conditions
	// Check that {subsystem} was replaced with "leader"
	// Check that {ptpInputPin} was replaced with "GNR-D_SDP0"
	// Check that {interface} was replaced with "eno5" (leading interface, not upstream port)
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
