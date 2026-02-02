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

	// DelayCompensation defines the delay compensation model (components, connections, routes)
	DelayCompensation *DelayCompensationModel `json:"delayCompensation,omitempty" yaml:"delayCompensation,omitempty"`
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

// DelayCompensationModel defines the three-layer delay compensation model:
// Topology (components), Physics (connections), and Intent (routes)
type DelayCompensationModel struct {
	// Components are static phase transfer nodes in the hardware topology
	Components []Component `json:"components,omitempty" yaml:"components,omitempty"`

	// Connections define physical connections (graph edges) with delay values
	Connections []Connection `json:"connections,omitempty" yaml:"connections,omitempty"`

	// Routes define compensation strategies for specific signal paths
	Routes []Route `json:"routes,omitempty" yaml:"routes,omitempty"`
}

// Component represents a static phase transfer node in the hardware topology
type Component struct {
	// ID is a unique identifier for this component (e.g., "DPLL 1kHz input")
	ID string `json:"id" yaml:"id"`

	// Description is an optional human-readable description
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// CompensationPoint is only required for components that can act as compensators
	CompensationPoint *CompensationPoint `json:"compensationPoint,omitempty" yaml:"compensationPoint,omitempty"`
}

// CompensationPoint defines where compensation can be applied
type CompensationPoint struct {
	// Name is the board label of the pin (e.g., "GNR-D_SDP0")
	Name string `json:"name" yaml:"name"`

	// Type specifies the compensation mechanism
	// "dpll" - apply via DPLL phase adjust API (default)
	// "FSWrite" - apply via sysfs write (future support)
	Type string `json:"type,omitempty" yaml:"type,omitempty"`
}

// Connection represents a physical connection (edge) in the topology graph
type Connection struct {
	// From is the source component ID
	From string `json:"from" yaml:"from"`

	// To is the destination component ID
	To string `json:"to" yaml:"to"`

	// Description is an optional human-readable description
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Getter specifies how to obtain the delay value from the platform (optional)
	Getter *DelayGetter `json:"getter,omitempty" yaml:"getter,omitempty"`

	// DelayPs is the delay in picoseconds (fallback default if getter fails or not provided)
	DelayPs int `json:"delayPs" yaml:"delayPs"`
}

// DelayGetter specifies how to obtain delay values from the platform
type DelayGetter struct {
	// FSRead reads delay from a sysfs file
	FSRead *FSReadGetter `json:"FSRead,omitempty" yaml:"FSRead,omitempty"`
}

// FSReadGetter reads delay from a sysfs file path
type FSReadGetter struct {
	// Path is the sysfs path, may contain {interface} placeholder for interface substitution
	Path string `json:"path" yaml:"path"`
}

// Route defines a compensation strategy for a specific signal path
type Route struct {
	// Name is a human-readable identifier for this route
	Name string `json:"name" yaml:"name"`

	// Description is an optional human-readable description
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Sequence is the ordered list of component IDs that form this route
	Sequence []string `json:"sequence" yaml:"sequence"`

	// Compensator is the component ID where compensation will be applied
	Compensator string `json:"compensator" yaml:"compensator"`
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
// It loads both defaults.yaml and delays.yaml (if available) and merges them.
// If labelMap is provided, board labels are remapped before returning.
func LoadHardwareDefaults(hwDefPath string, labelMap BoardLabelMap) (*HardwareDefaults, error) {
	if hwDefPath == "" {
		return nil, nil
	}

	var hd *HardwareDefaults
	var err error
	var delayModel *DelayCompensationModel

	// Load embedded defaults if available
	if data, ok := embeddedDefaults[hwDefPath]; ok && len(data) > 0 {
		glog.Infof("Hardware defaults: using embedded defaults for %s", hwDefPath)
		hd, err = decodeHardwareDefaults("embedded:"+hwDefPath, data)
		if err != nil {
			return nil, err
		}
	} else {
		hd = &HardwareDefaults{}
		glog.Infof("Hardware defaults: no embedded defaults for %s, creating empty", hwDefPath)
	}

	// Load and merge delay compensation model if available
	if delayData, ok := embeddedDelays[hwDefPath]; ok && len(delayData) > 0 {
		glog.Infof("Hardware defaults: loading delay compensation model for %s", hwDefPath)
		delayModel, err = decodeDelayCompensationModel("embedded:"+hwDefPath+"/delays.yaml", delayData)
		if err != nil {
			return nil, fmt.Errorf("failed to load delay compensation model for %s: %w", hwDefPath, err)
		}
		hd.DelayCompensation = delayModel
	} else {
		glog.V(4).Infof("Hardware defaults: no delay compensation model for %s", hwDefPath)
	}

	if len(labelMap) > 0 {
		RemapDefaultProfileLabels(hd, labelMap)
	}

	return hd, nil
}

func decodeHardwareDefaults(path string, data []byte) (*HardwareDefaults, error) {
	var hd HardwareDefaults
	if err := yaml.Unmarshal(data, &hd); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &hd, nil
}

func decodeDelayCompensationModel(path string, data []byte) (*DelayCompensationModel, error) {
	var dcm DelayCompensationModel
	if err := yaml.Unmarshal(data, &dcm); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &dcm, nil
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
// hwDefPath and clockType are guaranteed to be non-empty by callers (validated upstream).
func LoadBehaviorProfile(hwDefPath string, clockType string, loader *BoardLabelMapLoader) (*BehaviorProfileTemplate, error) {
	// Load optional board label remapping information from ConfigMap
	var labelMap BoardLabelMap
	labelMap, err := loader.LoadBoardLabelMap(hwDefPath)
	if err != nil {
		// Log but continue - remapping is optional (ConfigMap not found is not an error)
		glog.Warningf("Failed to load board label map for %s: %v (using embedded defaults)", hwDefPath, err)
	}

	clockTypeKey := strings.ToLower(clockType)

	// Load embedded behavior profiles
	if data, ok := embeddedBehaviorProfiles[hwDefPath]; ok && len(data) > 0 {
		glog.Infof("Behavior profile: loading embedded profiles for %s", hwDefPath)
		profiles, errDecode := decodeBehaviorProfiles("embedded:"+hwDefPath, data)
		if errDecode != nil {
			return nil, fmt.Errorf("failed to decode behavior profiles for %s: %w", hwDefPath, errDecode)
		}

		if template, found := profiles.BehaviorProfiles[clockTypeKey]; found {
			glog.Infof("Behavior profile: found template for %s/%s", hwDefPath, clockTypeKey)
			result := &template

			if len(labelMap) > 0 {
				RemapBehaviorProfileLabels(result, labelMap)
			}

			return result, nil
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
