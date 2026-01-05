package hardwareconfig

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"sigs.k8s.io/yaml"
)

//TODO: fix the CRD in the opeerator to replace hardwarePlugin by hardwareSpecificDefinitions

// HardwareDefaults is the YAML-backed spec defining static defaults/options for specific hardware.
type HardwareDefaults struct {
	// ClockIDTransformation defines how to transform serial number to clock ID for this hardware
	ClockIDTransformation *ClockIDTransformation `json:"clockIdTransformation,omitempty" yaml:"clockIdTransformation,omitempty"`

	// PinDefaults maps board labels to default priorities/states for EEC/PPS
	PinDefaults map[string]*PinDefault `json:"pinDefaults,omitempty" yaml:"pinDefaults,omitempty"`

	// ConnectorCommands defines commands to enable connectors as inputs, outputs or disable them
	ConnectorCommands *ConnectorCommands `json:"connectorCommands,omitempty" yaml:"connectorCommands,omitempty"`

	// PinEsyncCommands defines command sequences for enabling eSync on pins
	PinEsyncCommands *PinESyncCommands `json:"pinEsyncCommands,omitempty" yaml:"pinEsyncCommands,omitempty"`

	// InternalDelays defines connector<->pin internal delays for this hardware model
	InternalDelays *InternalDelays `json:"internalDelays,omitempty" yaml:"internalDelays,omitempty"`
}

// ClockIDTransformation defines how to convert serial number to clock ID
type ClockIDTransformation struct {
	// Method specifies the transformation algorithm
	// "direct" - use serial number bytes directly (e.g., Intel E810: keeps ff-ff in middle)
	// "eui64" - EUI-64 format: remove bytes 3-4 (ff-ff) and insert ff-fe
	Method string `json:"method,omitempty" yaml:"method,omitempty"`
}

// PinDefault represents default pin configuration settings
type PinDefault struct {
	EEC *PinDefaultEntry `json:"eec,omitempty"`
	PPS *PinDefaultEntry `json:"pps,omitempty"`
}

// PinDefaultEntry represents individual pin default settings for EEC or PPS
type PinDefaultEntry struct {
	Priority *int64 `json:"priority,omitempty"`
	State    string `json:"state,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

// ConnectorCommands groups actions per mode for device connectors
type ConnectorCommands struct {
	Outputs map[string]ConnectorAction `json:"outputs,omitempty"`
	Inputs  map[string]ConnectorAction `json:"inputs,omitempty"`
	Disable map[string]ConnectorAction `json:"disable,omitempty"`
}

// ConnectorAction is a list of low-level commands to execute for a connector
type ConnectorAction struct {
	Commands []ConnectorCommand `json:"commands"`
}

// ConnectorCommand represents a low-level action. Currently only FSWrite (sysfs write) is supported.
type ConnectorCommand struct {
	Type        string `json:"type"`
	Path        string `json:"path"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

// InternalDelays mirrors the structure from legacy addons for connector/pin delays.
type InternalDelays struct {
	PartType        string         `json:"partType"`
	ExternalInputs  []InternalLink `json:"externalInputs"`
	ExternalOutputs []InternalLink `json:"externalOutputs"`
	GnssInput       InternalLink   `json:"gnssInput"`
}

// InternalLink represents internal delay configuration between connectors and pins
type InternalLink struct {
	Connector string `json:"connector"`
	Pin       string `json:"pin"`
	DelayPs   int32  `json:"delayPs"`
}

// PinESyncCommands defines command sequences for configuring pins with eSync
type PinESyncCommands struct {
	Outputs []PinESyncCommand `json:"outputs,omitempty" yaml:"outputs,omitempty"`
	Inputs  []PinESyncCommand `json:"inputs,omitempty" yaml:"inputs,omitempty"`
}

// PinESyncCommand represents a single command in the eSync configuration sequence
type PinESyncCommand struct {
	Type             string                  `json:"type" yaml:"type"`
	Description      string                  `json:"description,omitempty" yaml:"description,omitempty"`
	Arguments        []string                `json:"arguments,omitempty" yaml:"arguments,omitempty"` // Arguments to set (minimum 1 if not using pinParentDevices)
	PinParentDevices []PinParentDeviceConfig `json:"pinParentDevices,omitempty" yaml:"pinParentDevices,omitempty"`
}

// PinParentDeviceConfig represents parent device configuration in eSync commands
type PinParentDeviceConfig struct {
	ParentDevice string `json:"parentDevice" yaml:"parentDevice"`
	State        string `json:"state" yaml:"state"`
}

// LoadHardwareDefaults loads defaults for a given hardware definition path (hwDefPath)
// from embedded defaults baked into the binary.
func LoadHardwareDefaults(hwDefPath string) (*HardwareDefaults, error) {
	if hwDefPath == "" {
		return nil, nil
	}

	// Prefer embedded defaults if available
	if data, ok := embeddedDefaults[hwDefPath]; ok && len(data) > 0 {
		glog.Infof("Hardware defaults: using embedded defaults for %s", hwDefPath)
		return decodeHardwareDefaults("embedded:"+hwDefPath, data)
	}

	glog.Infof("Hardware defaults: no embedded defaults for %s", hwDefPath)
	return nil, nil
}

func decodeHardwareDefaults(path string, data []byte) (*HardwareDefaults, error) {
	var hd HardwareDefaults
	if err := yaml.Unmarshal(data, &hd); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &hd, nil
}

// BehaviorProfileTemplate defines a template for behavior configuration for a specific clock type
type BehaviorProfileTemplate struct {
	// PinRoles maps role names to pin board labels (e.g., "ptpInputPin" -> "GNR-D_SDP0")
	PinRoles map[string]string `json:"pinRoles,omitempty" yaml:"pinRoles,omitempty"`

	// Sources defines template sources that will be instantiated
	Sources []ptpv2alpha1.SourceConfig `json:"sources,omitempty" yaml:"sources,omitempty"`

	// Conditions defines template conditions that will be instantiated
	Conditions []ptpv2alpha1.Condition `json:"conditions,omitempty" yaml:"conditions,omitempty"`
}

// BehaviorProfiles maps clock type (lowercase, e.g., "t-bc", "t-gm") to behavior template
type BehaviorProfiles struct {
	BehaviorProfiles map[string]BehaviorProfileTemplate `json:"behaviorProfiles" yaml:"behaviorProfiles"`
}

// LoadBehaviorProfile loads behavior profile template for a given hardware definition path and clock type
// Returns nil if no profile is found (not an error - templates are optional)
func LoadBehaviorProfile(hwDefPath string, clockType string) (*BehaviorProfileTemplate, error) {
	if hwDefPath == "" || clockType == "" {
		return nil, nil
	}

	// Normalize clock type to lowercase (e.g., "T-BC" -> "t-bc")
	clockTypeKey := strings.ToLower(clockType)

	// Load embedded behavior profiles
	if data, ok := embeddedBehaviorProfiles[hwDefPath]; ok && len(data) > 0 {
		glog.Infof("Behavior profile: loading embedded profiles for %s", hwDefPath)
		profiles, err := decodeBehaviorProfiles("embedded:"+hwDefPath, data)
		if err != nil {
			return nil, fmt.Errorf("failed to decode behavior profiles for %s: %w", hwDefPath, err)
		}

		if template, found := profiles.BehaviorProfiles[clockTypeKey]; found {
			glog.Infof("Behavior profile: found template for %s/%s", hwDefPath, clockTypeKey)
			return &template, nil
		}

		glog.Infof("Behavior profile: no template found for clock type %s in %s", clockTypeKey, hwDefPath)
		return nil, nil
	}

	glog.Infof("Behavior profile: no embedded profiles found for %s", hwDefPath)
	return nil, nil
}

func decodeBehaviorProfiles(path string, data []byte) (*BehaviorProfiles, error) {
	var bp BehaviorProfiles
	if err := yaml.Unmarshal(data, &bp); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &bp, nil
}

// --- End of vendor loader ---
