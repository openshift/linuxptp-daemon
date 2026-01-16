package hardwareconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
	ptpnetwork "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/network"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"sigs.k8s.io/yaml"
)

// LeadingInterfaceResolver resolves the leading interface from an upstream port
// This interface allows for dependency injection and testing
type LeadingInterfaceResolver interface {
	// GetPhcID returns the PHC ID (e.g., "/dev/ptp0") for the given interface
	GetPhcID(iface string) string
	// Readlink reads a symlink and returns the target path
	Readlink(path string) (string, error)
	// ReadDir reads a directory and returns directory entries
	ReadDir(path string) ([]os.DirEntry, error)
}

// realLeadingInterfaceResolver implements LeadingInterfaceResolver using real system calls
type realLeadingInterfaceResolver struct{}

func (r *realLeadingInterfaceResolver) GetPhcID(iface string) string {
	return ptpnetwork.GetPhcId(iface)
}

func (r *realLeadingInterfaceResolver) Readlink(path string) (string, error) {
	return os.Readlink(path)
}

func (r *realLeadingInterfaceResolver) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

// Global resolver (can be swapped for testing)
var leadingInterfaceResolver LeadingInterfaceResolver = &realLeadingInterfaceResolver{}

// SetLeadingInterfaceResolver allows tests to inject a mock resolver
func SetLeadingInterfaceResolver(resolver LeadingInterfaceResolver) {
	leadingInterfaceResolver = resolver
}

// ResetLeadingInterfaceResolver resets to the default real resolver
func ResetLeadingInterfaceResolver() {
	leadingInterfaceResolver = &realLeadingInterfaceResolver{}
}

// extractUpstreamPortsFromPtpProfile extracts upstream ports (interfaces with masterOnly=0) from a PTP profile.
// These are the PTP time receiver interfaces used for event detection.
func extractUpstreamPortsFromPtpProfile(ptpProfile *ptpv1.PtpProfile) []string {
	if ptpProfile == nil || ptpProfile.Ptp4lConf == nil {
		return nil
	}

	var upstreamPorts []string
	var currentSection string

	for _, line := range strings.Split(*ptpProfile.Ptp4lConf, "\n") {
		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check for section header (e.g., [eno2] or [global])
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.Trim(line, "[]")
			// Skip global and other non-interface sections
			if currentSection == "global" || currentSection == "nmea" || currentSection == "unicast" {
				currentSection = ""
			}
			continue
		}

		// Check for masterOnly=0 in current section
		if currentSection != "" {
			parts := strings.Fields(line)
			if len(parts) >= 2 && parts[0] == "masterOnly" && parts[1] == "0" {
				upstreamPorts = append(upstreamPorts, currentSection)
			}
		}
	}

	return upstreamPorts
}

// findLeadingInterfaceFromUpstreamPort finds the leading interface (for DPLL clock ID) from an upstream port.
// Steps:
// 1. Get PHC index from upstream port (ethtool -T)
// 2. Find PCI address from PHC symlink (/sys/class/ptp/ptp{N}/device)
// 3. Find leading interface from PCI address (/sys/bus/pci/devices/{pci}/net/)
func findLeadingInterfaceFromUpstreamPort(upstreamPort string) (string, error) {
	return findLeadingInterfaceFromUpstreamPortWithResolver(upstreamPort, leadingInterfaceResolver)
}

// findLeadingInterfaceFromUpstreamPortWithResolver is the internal implementation that accepts a resolver
// This allows for dependency injection and testing
func findLeadingInterfaceFromUpstreamPortWithResolver(upstreamPort string, resolver LeadingInterfaceResolver) (string, error) {
	if upstreamPort == "" {
		return "", fmt.Errorf("upstreamPort is empty")
	}

	if resolver == nil {
		resolver = &realLeadingInterfaceResolver{}
	}

	// Step 1: Get PHC index using resolver
	phcID := resolver.GetPhcID(upstreamPort)
	if phcID == "" {
		return "", fmt.Errorf("failed to get PHC ID for upstream port %s", upstreamPort)
	}

	// Extract PHC index from /dev/ptp{N} format
	phcIndex := strings.TrimPrefix(phcID, "/dev/ptp")
	if phcIndex == phcID {
		return "", fmt.Errorf("unexpected PHC ID format: %s", phcID)
	}

	// Step 2: Find PCI address from PHC symlink
	ptpDevicePath := fmt.Sprintf("/sys/class/ptp/ptp%s/device", phcIndex)
	pciPath, err := resolver.Readlink(ptpDevicePath)
	if err != nil {
		return "", fmt.Errorf("failed to read symlink %s: %w", ptpDevicePath, err)
	}

	// Resolve relative path (e.g., "../../../0000:13:00.0" -> "0000:13:00.0")
	pciAddress := filepath.Base(pciPath)
	if pciAddress == "" || pciAddress == "." {
		return "", fmt.Errorf("invalid PCI path from symlink: %s -> %s", ptpDevicePath, pciPath)
	}

	// Step 3: Find leading interface from PCI address
	netDir := fmt.Sprintf("/sys/bus/pci/devices/%s/net", pciAddress)
	entries, err := resolver.ReadDir(netDir)
	if err != nil {
		return "", fmt.Errorf("failed to read net directory %s: %w", netDir, err)
	}

	if len(entries) == 0 {
		return "", fmt.Errorf("no network interfaces found for PCI device %s", pciAddress)
	}

	// Return the first interface found (there should typically be only one)
	leadingInterface := entries[0].Name()
	glog.Infof("Found leading interface %s for upstream port %s (PHC: %s, PCI: %s)",
		leadingInterface, upstreamPort, phcID, pciAddress)

	return leadingInterface, nil
}

// ResolveClockChain resolves a minimal hardwareconfig by deriving structure and behavior
// from ptpconfig and behavior profile templates
func ResolveClockChain(hwConfig *ptpv2alpha1.HardwareConfig, ptpConfig *ptpv1.PtpConfig) (*ptpv2alpha1.HardwareConfig, error) {
	if hwConfig == nil {
		return nil, fmt.Errorf("hardwareconfig is nil")
	}

	if hwConfig.Spec.Profile.ClockType == nil {
		// No clockType specified, return as-is (no resolution needed)
		return hwConfig, nil
	}

	clockType := *hwConfig.Spec.Profile.ClockType
	// Create a deep copy to avoid modifying the original
	resolvedConfig := hwConfig.DeepCopy()

	// Find matching PTP profile by name
	// Each HardwareConfig has RelatedPtpProfileName that specifies which PtpProfile to use
	var ptpProfile *ptpv1.PtpProfile
	if ptpConfig != nil {
		relatedProfileName := hwConfig.Spec.RelatedPtpProfileName
		if relatedProfileName == "" {
			return nil, fmt.Errorf("hardwareconfig %s has clockType but no relatedPtpProfileName specified", hwConfig.Name)
		}

		// Search through all profiles in PtpConfig to find the matching one
		for i := range ptpConfig.Spec.Profile {
			if ptpConfig.Spec.Profile[i].Name != nil &&
				*ptpConfig.Spec.Profile[i].Name == relatedProfileName {
				ptpProfile = &ptpConfig.Spec.Profile[i]
				glog.Infof("Found matching PTP profile '%s' for hardwareconfig '%s'", relatedProfileName, hwConfig.Name)
				break
			}
		}

		if ptpProfile == nil {
			// List available profile names for better error message
			availableProfiles := make([]string, 0, len(ptpConfig.Spec.Profile))
			for _, p := range ptpConfig.Spec.Profile {
				if p.Name != nil {
					availableProfiles = append(availableProfiles, *p.Name)
				}
			}
			return nil, fmt.Errorf("PTP profile '%s' (specified in hardwareconfig '%s') not found in PtpConfig. Available profiles: %v",
				relatedProfileName, hwConfig.Name, availableProfiles)
		}
	} else {
		return nil, fmt.Errorf("PtpConfig is nil but required for hardwareconfig '%s' with clockType '%s'", hwConfig.Name, clockType)
	}

	// Resolve each subsystem
	for i := range resolvedConfig.Spec.Profile.ClockChain.Structure {
		subsystem := &resolvedConfig.Spec.Profile.ClockChain.Structure[i]

		// Derive structure from ptpconfig if not explicitly provided
		if subsystem.DPLL.NetworkInterface == "" || len(subsystem.DPLL.PhaseInputs) == 0 || len(subsystem.Ethernet) == 0 {
			if err := deriveSubsystemStructure(subsystem, ptpProfile, clockType); err != nil {
				return nil, fmt.Errorf("failed to derive structure for subsystem %s: %w", subsystem.Name, err)
			}
		}

		// Inject pins from delay compensation model into PhaseOutputs/PhaseInputs
		if err := injectDelayCompensationPins(subsystem); err != nil {
			glog.Warningf("Failed to inject delay compensation pins for subsystem %s: %v", subsystem.Name, err)
			// Continue even if injection fails
		}
	}

	// Derive behavior from templates if not explicitly provided
	if resolvedConfig.Spec.Profile.ClockChain.Behavior == nil {
		if err := deriveBehavior(resolvedConfig, clockType); err != nil {
			return nil, fmt.Errorf("failed to derive behavior: %w", err)
		}
	}

	// Print resolved configuration for debugging/verification
	printResolvedHardwareConfig(resolvedConfig)

	return resolvedConfig, nil
}

// printResolvedHardwareConfig prints the resolved hardwareconfig in YAML format
func printResolvedHardwareConfig(hwConfig *ptpv2alpha1.HardwareConfig) {
	if hwConfig == nil {
		return
	}

	// Marshal to YAML for readable output
	yamlData, err := yaml.Marshal(hwConfig)
	if err != nil {
		glog.Warningf("Failed to marshal resolved hardwareconfig to YAML: %v", err)
		return
	}

	glog.Infof("=== Resolved HardwareConfig '%s' ===", hwConfig.Name)
	glog.Infof("Resolved configuration:\n%s", string(yamlData))
	glog.Infof("=== End of Resolved HardwareConfig '%s' ===", hwConfig.Name)
}

// deriveSubsystemStructure derives DPLL and Ethernet configuration from ptpconfig and vendor defaults
func deriveSubsystemStructure(subsystem *ptpv2alpha1.Subsystem, ptpProfile *ptpv1.PtpProfile, clockType string) error {
	if ptpProfile == nil {
		return fmt.Errorf("ptpProfile is required for structure derivation")
	}

	hwDefPath := subsystem.HardwareSpecificDefinitions
	if hwDefPath == "" {
		return fmt.Errorf("hardwareSpecificDefinitions is required for structure derivation")
	}

	// Extract upstream ports from ptpconfig (PTP time receivers for event detection)
	upstreamPorts := extractUpstreamPortsFromPtpProfile(ptpProfile)
	if len(upstreamPorts) == 0 {
		return fmt.Errorf("no upstream ports found in ptpconfig")
	}

	// Find leading interface from first upstream port (for DPLL clock ID)
	var leadingInterface string
	if subsystem.DPLL.NetworkInterface == "" {
		var err error
		leadingInterface, err = findLeadingInterfaceFromUpstreamPort(upstreamPorts[0])
		if err != nil {
			return fmt.Errorf("failed to find leading interface from upstream port %s: %w", upstreamPorts[0], err)
		}
		subsystem.DPLL.NetworkInterface = leadingInterface
		glog.Infof("Derived NetworkInterface: %s (from upstream port %s)", leadingInterface, upstreamPorts[0])
	}

	// Load behavior profile to get pin roles
	behaviorTemplate, err := LoadBehaviorProfile(hwDefPath, clockType)
	if err != nil {
		return fmt.Errorf("failed to load behavior profile: %w", err)
	}
	if behaviorTemplate == nil {
		return fmt.Errorf("no behavior profile found for %s/%s", hwDefPath, clockType)
	}

	// Derive PhaseInputs from pinRoles
	if len(subsystem.DPLL.PhaseInputs) == 0 {
		ptpInputPin := behaviorTemplate.PinRoles["ptpInputPin"]
		if ptpInputPin == "" {
			return fmt.Errorf("ptpInputPin not found in pinRoles for %s/%s", hwDefPath, clockType)
		}

		if subsystem.DPLL.PhaseInputs == nil {
			subsystem.DPLL.PhaseInputs = make(map[string]ptpv2alpha1.PinConfig)
		}
		freq := int64(1) // 1 PPS for PTP
		subsystem.DPLL.PhaseInputs[ptpInputPin] = ptpv2alpha1.PinConfig{
			Frequency:   &freq,
			Description: "PTP time receiver input",
		}
		glog.Infof("Derived PhaseInputs: %s (frequency: %d Hz)", ptpInputPin, freq)
	}

	// Derive Ethernet ports (all upstream ports)
	if len(subsystem.Ethernet) == 0 {
		subsystem.Ethernet = []ptpv2alpha1.Ethernet{
			{
				Ports: upstreamPorts,
			},
		}
		glog.Infof("Derived Ethernet ports: %v", upstreamPorts)
	}

	return nil
}

// injectDelayCompensationPins injects pins from delay compensation model into PhaseOutputs/PhaseInputs
// if they don't already exist. This ensures pins referenced in delays.yaml are available for phase adjustment population.
func injectDelayCompensationPins(subsystem *ptpv2alpha1.Subsystem) error {
	hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
	if hwDefPath == "" {
		return nil // No hardware definition, nothing to inject
	}

	// Load hardware defaults to get delay compensation model
	hwDefaults, err := LoadHardwareDefaults(hwDefPath)
	if err != nil {
		return fmt.Errorf("failed to load hardware defaults for %s: %w", hwDefPath, err)
	}
	if hwDefaults == nil || hwDefaults.DelayCompensation == nil {
		return nil // No delay compensation model, nothing to inject
	}

	model := hwDefaults.DelayCompensation
	injectedCount := 0

	// Iterate through components and inject pins with compensation points
	for _, component := range model.Components {
		if component.CompensationPoint == nil {
			continue
		}
		if component.CompensationPoint.Type != "dpll" {
			continue
		}

		pinLabel := component.CompensationPoint.Name
		if pinLabel == "" {
			continue
		}

		// Determine if this should be an input or output pin
		// For now, assume outputs (OCP pins are outputs)
		// TODO: resolve the direction by correlating with the pin cache. Do it when implementing phase adjustment granularity
		isOutput := true
		if strings.Contains(strings.ToLower(component.ID), "input") {
			isOutput = false
		}

		if isOutput {
			// Add to PhaseOutputs if not already present
			if subsystem.DPLL.PhaseOutputs == nil {
				subsystem.DPLL.PhaseOutputs = make(map[string]ptpv2alpha1.PinConfig)
			}
			if _, exists := subsystem.DPLL.PhaseOutputs[pinLabel]; !exists {
				subsystem.DPLL.PhaseOutputs[pinLabel] = ptpv2alpha1.PinConfig{}
				glog.Infof("Injected delay compensation pin %s into PhaseOutputs for subsystem %s", pinLabel, subsystem.Name)
				injectedCount++
			}
		} else {
			// Add to PhaseInputs if not already present
			if subsystem.DPLL.PhaseInputs == nil {
				subsystem.DPLL.PhaseInputs = make(map[string]ptpv2alpha1.PinConfig)
			}
			if _, exists := subsystem.DPLL.PhaseInputs[pinLabel]; !exists {
				subsystem.DPLL.PhaseInputs[pinLabel] = ptpv2alpha1.PinConfig{}
				glog.Infof("Injected delay compensation pin %s into PhaseInputs for subsystem %s", pinLabel, subsystem.Name)
				injectedCount++
			}
		}
	}

	if injectedCount > 0 {
		glog.Infof("Injected %d delay compensation pins for subsystem %s", injectedCount, subsystem.Name)
	}

	return nil
}

// templateVariables holds the values for template variable resolution
type templateVariables struct {
	subsystem     string // subsystem name (e.g., "leader")
	ptpInputPin   string // pin board label from pinRoles (e.g., "GNR-D_SDP0")
	gnssInputPin  string // GNSS input pin label if provided (e.g., "GNSS_1PPS_IN")
	interfaceName string // leading interface name (e.g., "eno5")
}

// resolveTemplateString replaces template variables in a string
func resolveTemplateString(s string, vars templateVariables) string {
	result := s
	result = strings.ReplaceAll(result, "{subsystem}", vars.subsystem)
	result = strings.ReplaceAll(result, "{ptpInputPin}", vars.ptpInputPin)
	result = strings.ReplaceAll(result, "{gnssInputPin}", vars.gnssInputPin)
	result = strings.ReplaceAll(result, "{interface}", vars.interfaceName)
	return result
}

// deepCopyAndResolveSourceConfig deep copies a SourceConfig and resolves template variables
func deepCopyAndResolveSourceConfig(src ptpv2alpha1.SourceConfig, vars templateVariables, upstreamPorts []string) ptpv2alpha1.SourceConfig {
	// Deep copy using JSON marshal/unmarshal
	data, _ := json.Marshal(src)
	var resolved ptpv2alpha1.SourceConfig
	_ = json.Unmarshal(data, &resolved)

	// Resolve template variables
	resolved.Subsystem = resolveTemplateString(resolved.Subsystem, vars)
	resolved.BoardLabel = resolveTemplateString(resolved.BoardLabel, vars)

	// Add ptpTimeReceivers for ptpTimeReceiver sources
	if resolved.SourceType == "ptpTimeReceiver" && len(upstreamPorts) > 0 {
		resolved.PTPTimeReceivers = make([]string, len(upstreamPorts))
		copy(resolved.PTPTimeReceivers, upstreamPorts)
	}

	return resolved
}

// deepCopyAndResolveCondition deep copies a Condition and resolves template variables
func deepCopyAndResolveCondition(cond ptpv2alpha1.Condition, vars templateVariables) ptpv2alpha1.Condition {
	// Deep copy using JSON marshal/unmarshal
	data, _ := json.Marshal(cond)
	var resolved ptpv2alpha1.Condition
	_ = json.Unmarshal(data, &resolved)

	// Resolve template variables in desired states
	for i := range resolved.DesiredStates {
		ds := &resolved.DesiredStates[i]

		// Resolve DPLL desired state
		if ds.DPLL != nil {
			ds.DPLL.Subsystem = resolveTemplateString(ds.DPLL.Subsystem, vars)
			ds.DPLL.BoardLabel = resolveTemplateString(ds.DPLL.BoardLabel, vars)
		}
	}

	return resolved
}

// deriveBehavior derives behavior section from templates
func deriveBehavior(hwConfig *ptpv2alpha1.HardwareConfig, clockType string) error {
	clockChain := hwConfig.Spec.Profile.ClockChain

	// If behavior is already provided, merge with templates (future enhancement)
	// For now, if behavior exists, skip derivation
	if clockChain.Behavior != nil && (len(clockChain.Behavior.Sources) > 0 || len(clockChain.Behavior.Conditions) > 0) {
		glog.Infof("Behavior already provided, skipping template derivation")
		return nil
	}

	// Initialize behavior if nil
	if clockChain.Behavior == nil {
		clockChain.Behavior = &ptpv2alpha1.Behavior{}
	}

	// Process each subsystem to derive behavior
	for _, subsystem := range clockChain.Structure {
		hwDefPath := subsystem.HardwareSpecificDefinitions
		if hwDefPath == "" {
			continue // Skip subsystems without hardware definitions
		}

		// Load behavior template
		behaviorTemplate, err := LoadBehaviorProfile(hwDefPath, clockType)
		if err != nil {
			return fmt.Errorf("failed to load behavior profile for %s/%s: %w", hwDefPath, clockType, err)
		}
		if behaviorTemplate == nil {
			glog.Infof("No behavior template found for %s/%s, skipping", hwDefPath, clockType)
			continue
		}

		// Prepare template variables
		vars := templateVariables{
			subsystem:     subsystem.Name,
			ptpInputPin:   behaviorTemplate.PinRoles["ptpInputPin"],
			gnssInputPin:  behaviorTemplate.PinRoles["gnssInputPin"],
			interfaceName: subsystem.DPLL.NetworkInterface,
		}

		if vars.ptpInputPin == "" {
			glog.Warningf("ptpInputPin not found in pinRoles for %s/%s", hwDefPath, clockType)
		}
		if vars.gnssInputPin == "" {
			glog.Infof("gnssInputPin not found in pinRoles for %s/%s (will leave placeholders if present)", hwDefPath, clockType)
		}

		// Get upstream ports for PTP sources (from Ethernet ports)
		var upstreamPorts []string
		if len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
			upstreamPorts = subsystem.Ethernet[0].Ports
		}

		// Instantiate sources from template
		for _, sourceTemplate := range behaviorTemplate.Sources {
			resolvedSource := deepCopyAndResolveSourceConfig(sourceTemplate, vars, upstreamPorts)
			clockChain.Behavior.Sources = append(clockChain.Behavior.Sources, resolvedSource)
			glog.Infof("Instantiated source: %s (subsystem: %s, boardLabel: %s)",
				resolvedSource.Name, resolvedSource.Subsystem, resolvedSource.BoardLabel)
		}

		// Instantiate conditions from template
		for _, conditionTemplate := range behaviorTemplate.Conditions {
			resolvedCondition := deepCopyAndResolveCondition(conditionTemplate, vars)
			clockChain.Behavior.Conditions = append(clockChain.Behavior.Conditions, resolvedCondition)
			glog.Infof("Instantiated condition: %s", resolvedCondition.Name)
		}
	}

	glog.Infof("Derived behavior: %d sources, %d conditions",
		len(clockChain.Behavior.Sources), len(clockChain.Behavior.Conditions))

	return nil
}
