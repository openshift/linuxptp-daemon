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

// DetectStateChange processes a PTP4L log line and returns port-aware state change information.
// Returns the port name and condition type ("locked", "lost") for the port that changed state.
// Returns ("", "") when the log line contains no relevant state change or the port is not monitored.
// TODO: replace by pmc state monitor
func (psd *PTPStateDetector) DetectStateChange(logLine string) (portName string, conditionType string) {
	_, event, err := psd.ptp4lExtractor.Extract(logLine)
	if err != nil || event == nil {
		return "", ""
	}

	portName = psd.extractPortName(logLine)
	if portName == "" {
		return "", ""
	}

	if !psd.monitoredPorts[portName] {
		return "", ""
	}

	switch event.Role {
	case constants.PortRoleSlave:
		return portName, ConditionTypeLocked
	default:
		if strings.Contains(event.Raw, "SLAVE to ") {
			return portName, ConditionTypeLost
		}
		return "", ""
	}
}

// extractPortName extracts the port name from a PTP4L log line using the parser
func (psd *PTPStateDetector) extractPortName(logLine string) string {
	return psd.ExtractPortName(logLine)
}

// ExtractPortName extracts the port name from a PTP4L log line using the parser package
// This reuses the parser's extraction logic to ensure consistency
func (psd *PTPStateDetector) ExtractPortName(logLine string) string {
	return parser.ExtractPortName(logLine)
}

// GetSourcesForPort returns the source names that monitor the given port
func (psd *PTPStateDetector) GetSourcesForPort(portName string) []string {
	if sources, found := psd.portToSources[portName]; found {
		return sources
	}
	return []string{}
}

// buildCaches builds performance caches and compiles regexes for fast lookups
func (psd *PTPStateDetector) buildCaches() {
	// Initialize maps
	psd.portToSources = make(map[string][]string)
	psd.monitoredPorts = make(map[string]bool)

	// Build caches from hardware configs
	for _, hwConfig := range psd.hcm.hardwareConfigs {
		if hwConfig.Spec.Profile.ClockChain.Behavior != nil {
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
		if hwConfig.Spec.Profile.ClockChain.Behavior != nil {
			allConditions = append(allConditions, hwConfig.Spec.Profile.ClockChain.Behavior.Conditions...)
		}
	}

	return allConditions
}
