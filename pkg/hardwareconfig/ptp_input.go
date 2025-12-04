package hardwareconfig

import (
	"strings"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

// PTPStateDetector monitors PTP state changes based on hardware config behavior rules
type PTPStateDetector struct {
	hcm *HardwareConfigManager

	// Performance optimizations - cached lookups
	portToSources  map[string][]string // port -> list of source names that monitor it
	monitoredPorts map[string]bool     // set of all monitored ports

	// PTP4L parser for robust log parsing
	ptp4lExtractor parser.MetricsExtractor
}

// NewPTPStateDetector creates a new PTP state detector
func NewPTPStateDetector(hcm *HardwareConfigManager) *PTPStateDetector {
	psd := &PTPStateDetector{
		hcm: hcm,
	}

	// Initialize caches and compile regexes for performance
	psd.buildCaches()

	// Initialize PTP4L parser for robust log parsing
	psd.ptp4lExtractor = parser.NewPTP4LExtractor()

	return psd
}

// DetectStateChange processes a PTP4L log line and returns state change information
// Returns "locked", "lost", or "" (empty string for no relevant state change)
// Only returns a result if the interface is in the monitored sources list
// TODO: replace by pmc  state monitor
func (psd *PTPStateDetector) DetectStateChange(logLine string) string {
	// Use the robust PTP4L parser to extract event information
	_, event, err := psd.ptp4lExtractor.Extract(logLine)
	if err != nil || event == nil {
		return "" // Not a PTP4L event log line or parsing error
	}

	portName := psd.extractPortName(logLine)
	if portName == "" {
		return "" // No interface name found
	}

	if !psd.monitoredPorts[portName] {
		return "" // Port not in monitored sources, skip
	}

	switch event.Role {
	case constants.PortRoleSlave:
		return "locked"
	default:
		// Only treat as 'lost' when transitioning away from SLAVE explicitly
		// to avoid false positives on INIT/other non-loss transitions.
		if strings.Contains(event.Raw, "SLAVE to ") {
			return "lost"
		}
		return ""
	}
}

// extractPortName extracts the port name from a PTP4L log line using simple string parsing
func (psd *PTPStateDetector) extractPortName(logLine string) string {
	// Find the port pattern "] port N (interface):"
	portIndex := strings.Index(logLine, "] port ")
	if portIndex == -1 {
		return ""
	}

	// Find the interface name in parentheses after "port N"
	parenStart := strings.Index(logLine[portIndex:], "(")
	if parenStart == -1 {
		return ""
	}
	parenStart += portIndex

	parenEnd := strings.Index(logLine[parenStart:], ")")
	if parenEnd == -1 {
		return ""
	}
	parenEnd += parenStart

	// Extract and return port name
	return logLine[parenStart+1 : parenEnd]
}

// buildCaches builds performance caches and compiles regexes for fast lookups
func (psd *PTPStateDetector) buildCaches() {
	// Initialize maps
	psd.portToSources = make(map[string][]string)
	psd.monitoredPorts = make(map[string]bool)

	// Build caches from hardware configs
	for _, hwConfig := range psd.hcm.hardwareConfigs {
		if hwConfig.Spec.Profile.ClockChain != nil && hwConfig.Spec.Profile.ClockChain.Behavior != nil {
			for _, source := range hwConfig.Spec.Profile.ClockChain.Behavior.Sources {
				if source.SourceType == "ptpTimeReceiver" {
					for _, portName := range source.PTPTimeReceivers {
						// Add to monitored ports set
						psd.monitoredPorts[portName] = true

						// Add to port-to-sources mapping
						psd.portToSources[portName] = append(psd.portToSources[portName], source.Name)
					}
				}
			}
		}
	}
}

// GetMonitoredPorts returns all ports that are being monitored by hardware configs (optimized)
func (psd *PTPStateDetector) GetMonitoredPorts() []string {
	// Return cached result for O(1) performance
	ports := make([]string, 0, len(psd.monitoredPorts))
	for port := range psd.monitoredPorts {
		ports = append(ports, port)
	}
	return ports
}

// GetBehaviorRules returns all behavior rules from hardware configs
func (psd *PTPStateDetector) GetBehaviorRules() []ptpv2alpha1.Condition {
	var allConditions []ptpv2alpha1.Condition

	for _, hwConfig := range psd.hcm.hardwareConfigs {
		if hwConfig.Spec.Profile.ClockChain != nil && hwConfig.Spec.Profile.ClockChain.Behavior != nil {
			allConditions = append(allConditions, hwConfig.Spec.Profile.ClockChain.Behavior.Conditions...)
		}
	}

	return allConditions
}
