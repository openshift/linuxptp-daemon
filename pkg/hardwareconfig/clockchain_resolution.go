package hardwareconfig

import (
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

// UpstreamPortsFromPtpProfile extracts upstream ports (interfaces with masterOnly=0) from a PTP profile.
// These are the PTP time receiver interfaces used for event detection.
func UpstreamPortsFromPtpProfile(ptpProfile *ptpv1.PtpProfile) []string {
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

// extractInterfacesFromPtpProfile extracts all interface names from a PTP profile's
// ptp4l config (any [section] that is not a well-known non-interface section).
func extractInterfacesFromPtpProfile(ptpProfile *ptpv1.PtpProfile) []string {
	if ptpProfile == nil || ptpProfile.Ptp4lConf == nil {
		return nil
	}

	var interfaces []string
	for _, line := range strings.Split(*ptpProfile.Ptp4lConf, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section := strings.Trim(line, "[]")
			switch section {
			case "global", "nmea", "unicast":
				continue
			default:
				interfaces = append(interfaces, section)
			}
		}
	}
	return interfaces
}

// findLeadingInterfaceFromPort finds the leading interface (for DPLL clock ID) from any network port.
// Steps:
// 1. Get PHC index from port (ethtool -T)
// 2. Find PCI address from PHC symlink (/sys/class/ptp/ptp{N}/device)
// 3. Find leading interface from PCI address (/sys/bus/pci/devices/{pci}/net/)
func findLeadingInterfaceFromPort(port string) (string, error) {
	return findLeadingInterfaceFromPortWithResolver(port, leadingInterfaceResolver)
}

// findLeadingInterfaceFromPortWithResolver is the internal implementation that accepts a resolver.
// This allows for dependency injection and testing.
func findLeadingInterfaceFromPortWithResolver(port string, resolver LeadingInterfaceResolver) (string, error) {
	if port == "" {
		return "", fmt.Errorf("port is empty")
	}

	if resolver == nil {
		resolver = &realLeadingInterfaceResolver{}
	}

	// Step 1: Get PHC index using resolver
	phcID := resolver.GetPhcID(port)
	if phcID == "" {
		return "", fmt.Errorf("failed to get PHC ID for port %s", port)
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
	glog.Infof("Found leading interface %s for port %s (PHC: %s, PCI: %s)",
		leadingInterface, port, phcID, pciAddress)

	return leadingInterface, nil
}

// ProfileNamesMatch reports whether a PtpConfig profile name (stored) matches a
// HardwareConfig's RelatedPtpProfileName (requested).
func ProfileNamesMatch(stored, requested string) bool {
	if strings.TrimSpace(requested) == "" {
		return false
	}
	parts := strings.SplitN(stored, "_", 2)
	if len(parts) > 1 && parts[1] == requested {
		return true
	}
	return false
}

// ResolveClockChain resolves a minimal hardwareconfig by deriving structure and behavior
// from ptpconfig and behavior profile templates.
// Board label remapping from ConfigMap will be applied if configMapLoader is set.
func (hcm *HardwareConfigManager) ResolveClockChain(hwConfig *ptpv2alpha1.HardwareConfig, ptpConfig *ptpv1.PtpConfig) (*ptpv2alpha1.HardwareConfig, error) {
	if hwConfig == nil {
		return nil, fmt.Errorf("hardwareconfig is nil")
	}

	if hwConfig.Spec.Profile.ClockType == nil {
		return nil, fmt.Errorf("hardwareconfig %s has no clockType specified", hwConfig.Name)
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

		// Search through all profiles in PtpConfig to find the matching one.
		for i := range ptpConfig.Spec.Profile {
			if ptpConfig.Spec.Profile[i].Name == nil {
				continue
			}
			name := *ptpConfig.Spec.Profile[i].Name
			if ProfileNamesMatch(name, relatedProfileName) {
				ptpProfile = &ptpConfig.Spec.Profile[i]
				glog.Infof("Found matching PTP profile '%s' for hardwareconfig '%s'", name, hwConfig.Name)
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

		// Load hardware defaults once per subsystem (result is cached by getHardwareDefaults).
		// Use them here to detect unmanaged DPLLs (e.g., E830) so that extractDPLLFlags
		// can reuse the cached entry later without a redundant load.
		// Unmanaged DPLLs intentionally have no PhaseInputs/Ethernet or behavior templates.
		hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
		if hwDefPath != "" {
			hwDefaults, err := hcm.getHardwareDefaults(hwDefPath)
			if err != nil {
				glog.Warningf("Subsystem %s: failed to load hardware defaults from %s: %v", subsystem.Name, hwDefPath, err)
			} else if hwDefaults != nil {
				dpllFlags, flagErr := hwDefaults.ParseDPLLFlags()
				if flagErr != nil {
					glog.Warningf("Subsystem %s: failed to parse DPLL flags from %s: %v", subsystem.Name, hwDefPath, flagErr)
				} else if dpllFlags != 0 {
					glog.Infof("Subsystem %s is an unmanaged DPLL (has dpllFlags), skipping structure derivation", subsystem.Name)
					continue
				}
			}
		}

		// Derive structure from ptpconfig if not explicitly provided
		if subsystem.DPLL.NetworkInterface == "" || len(subsystem.DPLL.PhaseInputs) == 0 || len(subsystem.Ethernet) == 0 {
			if err := hcm.deriveSubsystemStructure(subsystem, ptpProfile, clockType); err != nil {
				return nil, fmt.Errorf("failed to derive structure for subsystem %s: %w", subsystem.Name, err)
			}
		}

		// Inject pins from delay compensation model into PhaseOutputs/PhaseInputs
		if err := hcm.injectDelayCompensationPins(subsystem); err != nil {
			glog.Warningf("Failed to inject delay compensation pins for subsystem %s: %v", subsystem.Name, err)
			// Continue even if injection fails
		}
	}

	// Derive behavior from templates, merging any user-provided sources.
	// Always run even when the user supplies Behavior.Sources (e.g. gnssConfig)
	// so the template's DPLL pin details and conditions are still applied.
	if err := hcm.deriveBehavior(resolvedConfig, clockType); err != nil {
		return nil, fmt.Errorf("failed to derive behavior: %w", err)
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
func (hcm *HardwareConfigManager) deriveSubsystemStructure(subsystem *ptpv2alpha1.Subsystem, ptpProfile *ptpv1.PtpProfile, clockType string) error {
	if ptpProfile == nil {
		return fmt.Errorf("ptpProfile is required for structure derivation")
	}

	hwDefPath := subsystem.HardwareSpecificDefinitions
	if hwDefPath == "" {
		return fmt.Errorf("hardwareSpecificDefinitions is required for structure derivation")
	}

	// Extract upstream ports from ptpconfig (PTP time receivers for event detection)
	upstreamPorts := UpstreamPortsFromPtpProfile(ptpProfile)
	if clockType != ClockTypeTGM {
		if len(upstreamPorts) == 0 {
			return fmt.Errorf("no upstream ports found in ptpconfig")
		}

		// Find leading interface from first upstream port (for DPLL clock ID)
		if subsystem.DPLL.NetworkInterface == "" {
			leadingInterface, err := findLeadingInterfaceFromPort(upstreamPorts[0])
			if err != nil {
				return fmt.Errorf("failed to find leading interface from upstream port %s: %w", upstreamPorts[0], err)
			}
			subsystem.DPLL.NetworkInterface = leadingInterface
			glog.Infof("Derived NetworkInterface: %s (from upstream port %s)", leadingInterface, upstreamPorts[0])
		}
	} else if subsystem.DPLL.NetworkInterface == "" {
		// T-GM has no upstream ports; derive from any interface in the PTP config
		allInterfaces := extractInterfacesFromPtpProfile(ptpProfile)
		if len(allInterfaces) == 0 {
			return fmt.Errorf("no interfaces found in ptpconfig for T-GM subsystem %s", subsystem.Name)
		}
		leadingInterface, err := findLeadingInterfaceFromPort(allInterfaces[0])
		if err != nil {
			return fmt.Errorf("failed to find leading interface from port %s: %w", allInterfaces[0], err)
		}
		subsystem.DPLL.NetworkInterface = leadingInterface
		glog.Infof("Derived NetworkInterface: %s (from T-GM port %s)", leadingInterface, allInterfaces[0])
	}

	// Load behavior profile to get pin roles
	behaviorTemplate, err := LoadBehaviorProfile(hwDefPath, clockType, hcm.configMapLoader)
	if err != nil {
		return fmt.Errorf("failed to load behavior profile: %w", err)
	}
	if behaviorTemplate == nil {
		return fmt.Errorf("no behavior profile found for %s/%s", hwDefPath, clockType)
	}

	if clockType != ClockTypeTGM {
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
	}

	return nil
}

// injectDelayCompensationPins injects pins from delay compensation model into PhaseOutputs/PhaseInputs
// if they don't already exist. This ensures pins referenced in delays.yaml are available for phase adjustment population.
func (hcm *HardwareConfigManager) injectDelayCompensationPins(subsystem *ptpv2alpha1.Subsystem) error {
	hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
	if hwDefPath == "" {
		return nil // No hardware definition, nothing to inject
	}

	hwDefaults, err := hcm.getHardwareDefaults(hwDefPath)
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
	resolved := src.DeepCopy()

	// Resolve template variables
	resolved.Subsystem = resolveTemplateString(resolved.Subsystem, vars)
	resolved.BoardLabel = resolveTemplateString(resolved.BoardLabel, vars)

	// Add ptpTimeReceivers for ptpTimeReceiver sources
	if resolved.SourceType == "ptpTimeReceiver" && len(upstreamPorts) > 0 {
		resolved.PTPTimeReceivers = make([]string, len(upstreamPorts))
		copy(resolved.PTPTimeReceivers, upstreamPorts)
	}

	return *resolved
}

// deepCopyAndResolveCondition deep copies a Condition and resolves template variables
func deepCopyAndResolveCondition(cond ptpv2alpha1.Condition, vars templateVariables) ptpv2alpha1.Condition {
	resolved := cond.DeepCopy()

	// Resolve template variables in desired states
	for i := range resolved.DesiredStates {
		ds := &resolved.DesiredStates[i]

		// Resolve DPLL desired state
		if ds.DPLL != nil {
			ds.DPLL.Subsystem = resolveTemplateString(ds.DPLL.Subsystem, vars)
			ds.DPLL.BoardLabel = resolveTemplateString(ds.DPLL.BoardLabel, vars)
		}
	}

	return *resolved
}

// deriveBehavior derives behavior section from templates
func (hcm *HardwareConfigManager) deriveBehavior(hwConfig *ptpv2alpha1.HardwareConfig, clockType string) error {
	clockChain := hwConfig.Spec.Profile.ClockChain

	// Save any user-provided sources for merging after template instantiation.
	// User sources can provide fields like gnssConfig that the template can't
	// know ahead of time, while the template provides the DPLL pin details
	// and conditions that the user shouldn't need to specify.
	var userSources []ptpv2alpha1.SourceConfig
	var userConditions []ptpv2alpha1.Condition
	if clockChain.Behavior != nil {
		userSources = clockChain.Behavior.Sources
		userConditions = clockChain.Behavior.Conditions
	}

	// Initialize/reset behavior for template derivation
	clockChain.Behavior = &ptpv2alpha1.Behavior{}

	// Process each subsystem to derive behavior
	for _, subsystem := range clockChain.Structure {
		hwDefPath := subsystem.HardwareSpecificDefinitions
		if hwDefPath == "" {
			continue // Skip subsystems without hardware definitions
		}

		// Load behavior template
		behaviorTemplate, err := LoadBehaviorProfile(hwDefPath, clockType, hcm.configMapLoader)
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

	// Merge user-provided source fields onto template-derived sources.
	// Match by Name first, then by SourceType. User fields (e.g., gnssConfig)
	// overlay template defaults without replacing DPLL/pin details.
	for _, userSource := range userSources {
		merged := false
		for i := range clockChain.Behavior.Sources {
			tplSource := &clockChain.Behavior.Sources[i]
			if (userSource.Name != "" && tplSource.Name == userSource.Name) ||
				(userSource.Name == "" && tplSource.SourceType == userSource.SourceType) {
				mergeSourceConfig(tplSource, &userSource)
				glog.Infof("Merged user source %q onto template source %q", userSource.Name, tplSource.Name)
				merged = true
				break
			}
		}
		if !merged {
			// User source doesn't match any template source — add it as-is
			clockChain.Behavior.Sources = append(clockChain.Behavior.Sources, userSource)
			glog.Infof("Added user source %q (no matching template)", userSource.Name)
		}
	}

	// Merge user-provided conditions onto template-derived conditions.
	// Match by name: user conditions override same-named template conditions.
	// Unmatched user conditions are appended.
	for _, userCond := range userConditions {
		merged := false
		for i := range clockChain.Behavior.Conditions {
			if clockChain.Behavior.Conditions[i].Name == userCond.Name {
				clockChain.Behavior.Conditions[i] = userCond
				glog.Infof("User condition %q overrides template condition", userCond.Name)
				merged = true
				break
			}
		}
		if !merged {
			clockChain.Behavior.Conditions = append(clockChain.Behavior.Conditions, userCond)
			glog.Infof("Added user condition %q (no matching template)", userCond.Name)
		}
	}

	glog.Infof("Derived behavior: %d sources, %d conditions",
		len(clockChain.Behavior.Sources), len(clockChain.Behavior.Conditions))

	return nil
}

// mergeSourceConfig overlays user-provided fields onto a template source.
// Only non-zero user fields are applied, preserving template defaults.
func mergeSourceConfig(tpl, user *ptpv2alpha1.SourceConfig) {
	if user.Subsystem != "" {
		tpl.Subsystem = user.Subsystem
	}
	if user.BoardLabel != "" {
		tpl.BoardLabel = user.BoardLabel
	}
	if len(user.PTPTimeReceivers) > 0 {
		tpl.PTPTimeReceivers = user.PTPTimeReceivers
	}
	if user.GNSSConfig != nil {
		// Explicitly shallow-copy the user GNSS config
		gnss := user.GNSSConfig.DeepCopy()
		if user.GNSSConfig.Match == nil && tpl.GNSSConfig != nil && tpl.GNSSConfig.Match != nil {
			// Fallback to the template matcher if no user matcher was provided
			gnss.Match = tpl.GNSSConfig.Match.DeepCopy()
		}
		tpl.GNSSConfig = gnss
	}
}
