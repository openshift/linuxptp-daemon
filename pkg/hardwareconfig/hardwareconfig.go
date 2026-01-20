// Package hardwareconfig provides hardware configuration management for the linuxptp daemon.
package hardwareconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	dpllcfg "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"k8s.io/client-go/kubernetes"

	// loader is part of this package (vendor_loader.go)
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

// Condition type constants
const (
	ConditionTypeDefault = "default"
	ConditionTypeInit    = "init"
	ConditionTypeLocked  = "locked"
	ConditionTypeLost    = "lost"
)

const (
	defaultProfileName  = "unnamed"
	ptpTimeReceiverType = "ptpTimeReceiver"
)

func collectConnectorUsage(subsystem ptpv2alpha1.Subsystem) (map[string]struct{}, map[string]struct{}) {
	inputs := make(map[string]struct{})
	outputs := make(map[string]struct{})

	add := func(connector string, target map[string]struct{}) {
		if connector == "" {
			return
		}
		target[connector] = struct{}{}
	}

	for _, cfg := range subsystem.DPLL.PhaseInputs {
		add(cfg.Connector, inputs)
	}
	for _, cfg := range subsystem.DPLL.FrequencyInputs {
		add(cfg.Connector, inputs)
	}
	for _, cfg := range subsystem.DPLL.PhaseOutputs {
		add(cfg.Connector, outputs)
	}
	for _, cfg := range subsystem.DPLL.FrequencyOutputs {
		add(cfg.Connector, outputs)
	}

	return inputs, outputs
}

func firstInterface(subsystem ptpv2alpha1.Subsystem) string {
	if len(subsystem.Ethernet) == 0 {
		return ""
	}
	if len(subsystem.Ethernet[0].Ports) == 0 {
		return ""
	}
	return subsystem.Ethernet[0].Ports[0]
}

func (hcm *HardwareConfigManager) translateConnectorCommands(commands *ConnectorCommands, defaultInterface, subsystemName string, connectorsInput, connectorsOutput map[string]struct{}) ([]SysFSCommand, error) {
	if commands == nil {
		return nil, nil
	}
	if defaultInterface == "" {
		glog.Infof("No Ethernet ports defined for subsystem %s; skipping connectorCommands translation", subsystemName)
		return nil, nil
	}

	render := func(cmds []ConnectorCommand) ([]SysFSCommand, error) {
		out := make([]SysFSCommand, 0, len(cmds))
		for _, c := range cmds {
			if !strings.EqualFold(c.Type, "FSWrite") {
				glog.Infof("Unsupported connector command type %s, skipping", c.Type)
				continue
			}
			path := strings.ReplaceAll(c.Path, "{interface}", defaultInterface)
			var paths []string
			var err error
			if strings.Contains(path, "ptp*") {
				paths, err = hcm.resolveSysFSPtpDevice(path)
				if err != nil {
					return nil, fmt.Errorf("resolve ptp* for %s: %w", path, err)
				}
			} else {
				paths = []string{path}
			}
			for _, rp := range paths {
				out = append(out, SysFSCommand{Path: rp, Value: c.Value, Description: c.Description})
			}
		}
		return out, nil
	}

	sysfs := make([]SysFSCommand, 0)

	apply := func(conn string, actionMap map[string]ConnectorAction, usage string) error {
		if actionMap == nil {
			glog.Infof("No %s commands defined for connector %s", usage, conn)
			return nil
		}
		action, ok := actionMap[conn]
		if !ok {
			glog.Infof("Connector %s used as %s but no vendor commands provided", conn, usage)
			return nil
		}
		rendered, err := render(action.Commands)
		if err != nil {
			return err
		}
		sysfs = append(sysfs, rendered...)
		return nil
	}

	for conn := range connectorsInput {
		if err := apply(conn, commands.Inputs, "input"); err != nil {
			return nil, fmt.Errorf("connector %s inputs: %w", conn, err)
		}
	}
	for conn := range connectorsOutput {
		if err := apply(conn, commands.Outputs, "output"); err != nil {
			return nil, fmt.Errorf("connector %s outputs: %w", conn, err)
		}
	}

	if commands.Disable != nil {
		defined := make(map[string]struct{})
		for name := range commands.Inputs {
			defined[name] = struct{}{}
		}
		for name := range commands.Outputs {
			defined[name] = struct{}{}
		}
		for name := range commands.Disable {
			defined[name] = struct{}{}
		}

		used := make(map[string]struct{})
		for name := range connectorsInput {
			used[name] = struct{}{}
		}
		for name := range connectorsOutput {
			used[name] = struct{}{}
		}

		for conn := range defined {
			if _, ok := used[conn]; ok {
				continue
			}
			action, exists := commands.Disable[conn]
			if !exists {
				glog.Infof("Connector %s not referenced but disable commands missing", conn)
				continue
			}
			rendered, err := render(action.Commands)
			if err != nil {
				return nil, fmt.Errorf("connector %s disable: %w", conn, err)
			}
			sysfs = append(sysfs, rendered...)
		}
	}

	return sysfs, nil
}

// HardwareConfigUpdateHandler defines the interface for handling hardware configuration updates
//
//nolint:revive // Name is part of established API
type HardwareConfigUpdateHandler interface {
	UpdateHardwareConfig(hwConfigs []ptpv2alpha1.HardwareConfig) error
}

// SysFSCommand represents a resolved sysFS command ready for execution
type SysFSCommand struct {
	Path        string // Resolved path (with interface names substituted)
	Value       string // Value to write
	Description string // Optional description for logging
}

type enrichedHardwareConfig struct {
	ptpv2alpha1.HardwareConfig
	dpllPinCommands map[string][]dpll.PinParentDeviceCtl
	sysFSCommands   map[string][]SysFSCommand // condition name -> resolved sysFS commands
	// Static defaults derived from the clock chain structure (hardware-specific)
	structurePinCommands   []dpll.PinParentDeviceCtl
	structureSysFSCommands []SysFSCommand
	// Holdover parameters mapped by clock ID
	holdoverParams map[uint64]*ptpv2alpha1.HoldoverParameters
}

// HardwareConfigManager manages hardware configurations and their application
//
//nolint:revive // Name is part of established API
type HardwareConfigManager struct {
	hardwareConfigs []enrichedHardwareConfig
	pinCache        *PinCache
	pinApplier      func([]dpll.PinParentDeviceCtl) error
	sysfsWriter     func(string, string) error
	// cache of hardware defaults keyed by hwDefPath to avoid repeated loads
	hwDefaultsCache map[string]*HardwareDefaults
	// cache of resolved clock IDs keyed by "interface:hwDefPath" to avoid repeated resolution
	clockIDCache map[string]uint64
	// current PtpConfig used for resolving clock chains (optional)
	ptpConfig *ptpv1.PtpConfig
	// ConfigMap loader for board label remapping (optional, can be nil)
	configMapLoader *BoardLabelMapLoader
	mu              sync.RWMutex
	cond            *sync.Cond
	ready           bool
}

// NewHardwareConfigManager creates a new hardware config manager.
func NewHardwareConfigManager(kubeClient kubernetes.Interface, namespace string) *HardwareConfigManager {
	hcm := &HardwareConfigManager{
		hardwareConfigs: make([]enrichedHardwareConfig, 0),
		pinApplier:      func(cmds []dpll.PinParentDeviceCtl) error { return BatchPinSet(&cmds) },
		hwDefaultsCache: make(map[string]*HardwareDefaults),
		clockIDCache:    make(map[string]uint64),
		sysfsWriter: func(path, value string) error {
			glog.Infof("Writing sysfs value to %s: %s", path, value)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return fmt.Errorf("create dir for %s: %w", path, err)
			}
			return os.WriteFile(path, []byte(value), 0o644)
		},
	}
	hcm.cond = sync.NewCond(&hcm.mu)

	// Set up ConfigMap loader for board label remapping
	configMapLoader := NewBoardLabelMapLoader(kubeClient, namespace)
	hcm.configMapLoader = configMapLoader

	return hcm
}

// SetConfigMapLoader sets the ConfigMap loader for board label remapping (optional)
func (hcm *HardwareConfigManager) SetConfigMapLoader(loader *BoardLabelMapLoader) {
	hcm.configMapLoader = loader
}

// getClockIDCached returns the clock ID for an interface, using cache to avoid repeated resolution
func (hcm *HardwareConfigManager) getClockIDCached(iface string, hwDefPath string) (uint64, error) {
	cacheKey := fmt.Sprintf("%s:%s", iface, hwDefPath)

	// Check cache first
	if clockID, found := hcm.clockIDCache[cacheKey]; found {
		glog.V(4).Infof("Using cached clock ID %#x for interface %s (hwDef: %s)", clockID, iface, hwDefPath)
		return clockID, nil
	}

	// Not in cache, resolve it
	clockID, err := GetClockIDFromInterfaceWithCache(iface, hwDefPath, hcm.pinCache)
	if err != nil {
		return 0, err
	}
	if clockID == 0 {
		return 0, fmt.Errorf("resolved clock ID is zero for interface %s (hwDef: %s)", iface, hwDefPath)
	}

	// Cache the result
	hcm.clockIDCache[cacheKey] = clockID
	glog.Infof("Cached clock ID %#x for interface %s (hwDef: %s)", clockID, iface, hwDefPath)

	return clockID, nil
}

// UpdateHardwareConfig implements HardwareConfigUpdateHandler interface
// This method updates the hardware configuration stored in the manager
func (hcm *HardwareConfigManager) UpdateHardwareConfig(hwConfigs []ptpv2alpha1.HardwareConfig) error {
	glog.Infof("Received hardware configuration update with %d hardware configs", len(hwConfigs))

	// Clear clock ID cache for new hardware config processing
	hcm.clockIDCache = make(map[string]uint64)

	// Detect removed hardwareconfigs before updating
	removedConfigs := hcm.detectRemovedHardwareConfigs(hwConfigs)
	if len(removedConfigs) > 0 {
		glog.Infof("Detected %d removed hardware configs, applying vendor defaults to reset DPLL", len(removedConfigs))
		if err := hcm.applyVendorDefaultsForRemovedConfigs(removedConfigs); err != nil {
			glog.Errorf("Failed to apply vendor defaults for removed hardware configs: %v", err)
			// Continue with update even if defaults application fails
		}
	}

	// Handle empty configs case - mark as ready immediately
	if len(hwConfigs) == 0 {
		hcm.setHardwareConfigs([]enrichedHardwareConfig{})
		return nil
	}

	var err error

	// Get DPLL pins for processing
	hcm.pinCache, err = GetDpllPins()
	if err != nil {
		return fmt.Errorf("failed to get DPLL pins: %w", err)
	}
	if hcm.pinCache != nil {
		glog.Infof("Pin cache initialized: %d total pins", hcm.pinCache.Count())
	}

	prepared := make([]enrichedHardwareConfig, len(hwConfigs))
	for i, hwConfig := range hwConfigs {
		// Resolve clock chain if clockType is specified
		resolvedConfig := &hwConfig
		if hwConfig.Spec.Profile.ClockType != nil {
			hcm.mu.RLock()
			ptpConfig := hcm.ptpConfig
			hcm.mu.RUnlock()
			resolvedConfig, err = hcm.ResolveClockChain(&hwConfig, ptpConfig)
			if err != nil {
				return fmt.Errorf("failed to resolve clock chain for hardware config %s: %w", hwConfig.Name, err)
			}
			glog.Infof("Resolved clock chain for hardware config '%s' (clockType: %s)", hwConfig.Name, *hwConfig.Spec.Profile.ClockType)
		}

		prepared[i] = enrichedHardwareConfig{HardwareConfig: *resolvedConfig}

		glog.Infof("Resolving hardware config '%s' (%d/%d)", resolvedConfig.Name, i+1, len(hwConfigs))

		// Populate phase adjustments from delay compensation model BEFORE resolving structure
		// so that phase adjustment commands can be built during structure resolution
		if populateErr := hcm.populatePhaseAdjustments(resolvedConfig); populateErr != nil {
			glog.Warningf("Failed to populate phase adjustments for hardware config %s: %v", resolvedConfig.Name, populateErr)
			// Continue even if phase adjustment population fails
		}

		dpllCommands, sysFSCommands, behaviorErr := hcm.resolveClockChainBehavior(*resolvedConfig)
		if behaviorErr != nil {
			return fmt.Errorf("failed to resolve clock chain behavior for hardware config %s: %w", resolvedConfig.Name, behaviorErr)
		}
		glog.Infof("  behavior: %d conditions with DPLL commands, %d conditions with sysfs commands", len(dpllCommands), len(sysFSCommands))
		prepared[i].dpllPinCommands = dpllCommands
		prepared[i].sysFSCommands = sysFSCommands

		structPins, structSysfs, structErr := hcm.resolveClockChainStructure(*resolvedConfig)
		if structErr != nil {
			return fmt.Errorf("failed to resolve clock chain structure for hardware config %s: %w", resolvedConfig.Name, structErr)
		}
		glog.Infof("  structure: %d DPLL commands, %d sysfs commands", len(structPins), len(structSysfs))
		prepared[i].structurePinCommands = structPins
		prepared[i].structureSysFSCommands = structSysfs

		// Update stored config with populated phase adjustments
		prepared[i].HardwareConfig = *resolvedConfig

		// Extract holdover parameters from subsystems
		holdoverParams := hcm.extractHoldoverParameters(*resolvedConfig)
		prepared[i].holdoverParams = holdoverParams
		if len(holdoverParams) > 0 {
			glog.Infof("  holdover: extracted parameters for %d DPLLs", len(holdoverParams))
		}
	}

	hcm.setHardwareConfigs(prepared)
	return nil
}

// SetPtpConfig sets the current PtpConfig used for resolving clock chains
// This should be called when PtpConfig is updated so that hardwareconfigs
// with clockType can be resolved using the related ptpProfile
func (hcm *HardwareConfigManager) SetPtpConfig(ptpConfig *ptpv1.PtpConfig) {
	hcm.mu.Lock()
	defer hcm.mu.Unlock()
	hcm.ptpConfig = ptpConfig
	if ptpConfig != nil {
		glog.Infof("Updated PtpConfig in HardwareConfigManager (%d profiles)", len(ptpConfig.Spec.Profile))
	} else {
		glog.Infof("Cleared PtpConfig in HardwareConfigManager")
	}
}

// CloneHardwareConfigs returns a deep copy of the current hardware configurations
func (hcm *HardwareConfigManager) CloneHardwareConfigs() []ptpv2alpha1.HardwareConfig {
	hcm.mu.RLock()
	defer hcm.mu.RUnlock()
	out := make([]ptpv2alpha1.HardwareConfig, len(hcm.hardwareConfigs))
	for i, cfg := range hcm.hardwareConfigs {
		out[i] = cfg.HardwareConfig
	}
	return out
}

// HasHardwareConfigForProfile checks if hardware config is available for a PTP profile
func (hcm *HardwareConfigManager) HasHardwareConfigForProfile(nodeProfile *ptpv1.PtpProfile) bool {
	if nodeProfile.Name == nil {
		return false
	}

	for _, hwConfig := range hcm.hardwareConfigs {
		if hwConfig.Spec.RelatedPtpProfileName == *nodeProfile.Name {
			return true
		}
	}
	return false
}

// GetHardwareConfigsForProfile returns hardware configs associated with a PTP profile
func (hcm *HardwareConfigManager) GetHardwareConfigsForProfile(nodeProfile *ptpv1.PtpProfile) []ptpv2alpha1.HardwareProfile {
	if nodeProfile.Name == nil {
		return nil
	}

	var relevantConfigs []ptpv2alpha1.HardwareProfile
	for _, hwConfig := range hcm.hardwareConfigs {
		if hwConfig.Spec.RelatedPtpProfileName == *nodeProfile.Name {
			relevantConfigs = append(relevantConfigs, hwConfig.Spec.Profile)
		}
	}
	return relevantConfigs
}

// ApplyHardwareConfigsForProfile applies hardware configurations for a PTP profile
// It processes "default" and "init" conditions in order, applying their desired states
func (hcm *HardwareConfigManager) ApplyHardwareConfigsForProfile(nodeProfile *ptpv1.PtpProfile) error {
	if nodeProfile.Name == nil {
		return fmt.Errorf("PTP profile has no name")
	}

	// Find enriched hardware configs for this profile
	var relevantConfigs []enrichedHardwareConfig
	for _, hwConfig := range hcm.hardwareConfigs {
		if hwConfig.Spec.RelatedPtpProfileName == *nodeProfile.Name {
			relevantConfigs = append(relevantConfigs, hwConfig)
		}
	}

	glog.Infof("Applying %d hardware configurations for PTP profile %s",
		len(relevantConfigs), *nodeProfile.Name)

	for _, enrichedConfig := range relevantConfigs {
		profileName := defaultProfileName
		if enrichedConfig.Spec.Profile.Name != nil {
			profileName = *enrichedConfig.Spec.Profile.Name
		}

		glog.Infof("Applying hardware profile: %s", profileName)

		if err := hcm.applyStructureDefaults(&enrichedConfig, profileName); err != nil {
			return err
		}
		if err := hcm.applyBehaviorConditions(&enrichedConfig, profileName); err != nil {
			return err
		}
	}

	// NOTE: Structure application currently resolves and caches commands, but execution relies on future netlink/sysfs writers.
	// Until full support lands, we simply cache the resolved data and return success so the daemon remains stable.

	// Enrich nodeProfile with settings required by the legacy runtime path:
	// - clockId[iface]: used by DPLL/ts2phc to bind to the right unit
	// - leadingInterface / upstreamPort: used by T-BC logic for event sources
	for _, enrichedConfig := range relevantConfigs {
		hcm.populatePtpSettingsFromHardware(nodeProfile, enrichedConfig.Spec.Profile)
	}
	return nil
}

// populatePtpSettingsFromHardware updates nodeProfile.PtpSettings with values derived from HardwareConfig:
// - clockId[<iface>] for each subsystem's resolved interface
// - leadingInterface and upstreamPort when determinable from behavior sources
func (hcm *HardwareConfigManager) populatePtpSettingsFromHardware(nodeProfile *ptpv1.PtpProfile, profile ptpv2alpha1.HardwareProfile) {
	if nodeProfile == nil {
		return
	}
	if nodeProfile.PtpSettings == nil {
		nodeProfile.PtpSettings = make(map[string]string)
	}
	cc := profile.ClockChain
	if cc == nil {
		return
	}

	// 1) Resolve and set clock IDs per subsystem interface
	for _, subsystem := range cc.Structure {
		networkInterface := subsystem.DPLL.NetworkInterface
		if networkInterface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
			networkInterface = subsystem.Ethernet[0].Ports[0]
		}
		if networkInterface == "" {
			continue
		}

		// Skip if already set to avoid overriding explicit config
		clockKey := fmt.Sprintf("%s[%s]", dpllcfg.ClockIdStr, networkInterface)
		if _, exists := nodeProfile.PtpSettings[clockKey]; exists {
			continue
		}

		hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
		clockID, err := hcm.getClockIDCached(networkInterface, hwDefPath)
		if err != nil {
			glog.V(2).Infof("populatePtpSettings: failed to resolve clock ID for %s (subsystem %s): %v", networkInterface, subsystem.Name, err)
			continue
		}
		nodeProfile.PtpSettings[clockKey] = strconv.FormatUint(clockID, 10)
		glog.Infof("populatePtpSettings: set %s=%s", clockKey, nodeProfile.PtpSettings[clockKey])
	}

	// 2) Derive leadingInterface and upstreamPort for T-BC-like configurations
	if cc.Behavior != nil {
		upstreamPort := ""
		for _, source := range cc.Behavior.Sources {
			if source.SourceType == "ptpTimeReceiver" && len(source.PTPTimeReceivers) > 0 {
				upstreamPort = source.PTPTimeReceivers[0]
				break
			}
		}
		if upstreamPort != "" {
			// Set upstreamPort if not present
			if _, ok := nodeProfile.PtpSettings["upstreamPort"]; !ok {
				nodeProfile.PtpSettings["upstreamPort"] = upstreamPort
				glog.Infof("populatePtpSettings: set upstreamPort=%s", upstreamPort)
			}
			// Resolve leadingInterface based on subsystem containing the upstreamPort
			if _, ok := nodeProfile.PtpSettings["leadingInterface"]; !ok {
				iface, err := hcm.getInterfaceNameFromSources("", cc)
				if err == nil && iface != nil && *iface != "" {
					nodeProfile.PtpSettings["leadingInterface"] = *iface
					glog.Infof("populatePtpSettings: set leadingInterface=%s", *iface)
				} else {
					glog.V(3).Infof("populatePtpSettings: could not resolve leadingInterface from behavior sources: %v", err)
				}
			}
		}
	}
}

// populatePhaseAdjustments populates PhaseAdjustment fields in HardwareConfig based on delay compensation model
func (hcm *HardwareConfigManager) populatePhaseAdjustments(hwConfig *ptpv2alpha1.HardwareConfig) error {
	cc := hwConfig.Spec.Profile.ClockChain
	glog.Infof("Populating phase adjustments for %d subsystems", len(cc.Structure))

	for _, subsystem := range cc.Structure {
		hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
		if hwDefPath == "" {
			glog.V(2).Infof("Subsystem %s: no hardware definition path, skipping", subsystem.Name)
			continue
		}

		glog.Infof("Processing subsystem %s with hardware definition %s", subsystem.Name, hwDefPath)

		// Load hardware defaults (includes delay compensation model)
		hwDefaults, err := hcm.getHardwareDefaults(hwDefPath)
		if err != nil {
			return fmt.Errorf("failed to load hardware defaults for %s: %w", hwDefPath, err)
		}
		if hwDefaults == nil {
			glog.V(2).Infof("Subsystem %s: no hardware defaults loaded, skipping", subsystem.Name)
			continue
		}
		if hwDefaults.DelayCompensation == nil {
			glog.V(2).Infof("Subsystem %s: no delay compensation model in hardware defaults, skipping", subsystem.Name)
			continue
		}

		// Get network interface for this subsystem
		networkInterface := subsystem.DPLL.NetworkInterface
		if networkInterface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
			networkInterface = subsystem.Ethernet[0].Ports[0]
		}
		if networkInterface == "" {
			glog.Infof("Subsystem %s: no network interface found, skipping phase adjustment population", subsystem.Name)
			continue
		}

		glog.Infof("Subsystem %s: calling PopulatePhaseAdjustmentsFromDelays with interface %s", subsystem.Name, networkInterface)
		// Populate phase adjustments for this subsystem
		if populateErr := PopulatePhaseAdjustmentsFromDelays(hwConfig, hwDefaults, networkInterface); populateErr != nil {
			return fmt.Errorf("failed to populate phase adjustments for subsystem %s: %w", subsystem.Name, populateErr)
		}
	}

	return nil
}

func (hcm *HardwareConfigManager) resolveClockChainBehavior(hwConfig ptpv2alpha1.HardwareConfig) (map[string][]dpll.PinParentDeviceCtl, map[string][]SysFSCommand, error) {
	clockChain := hwConfig.Spec.Profile.ClockChain
	if clockChain.Behavior == nil || len(clockChain.Behavior.Conditions) == 0 {
		glog.Infof("Hardware config %s has no behavior conditions", hwConfig.Name)
		return make(map[string][]dpll.PinParentDeviceCtl), make(map[string][]SysFSCommand), nil
	}

	// Cache ALL conditions by name
	glog.Infof("Hardware config %s behavior: resolving %d conditions", hwConfig.Name, len(clockChain.Behavior.Conditions))
	pinCommandsPerCondition := make(map[string][]dpll.PinParentDeviceCtl)
	sysFSCommandsPerCondition := make(map[string][]SysFSCommand)

	for _, condition := range clockChain.Behavior.Conditions {
		// Skip conditions without triggers (invalid)
		if len(condition.Triggers) == 0 {
			glog.Warningf("Skipping condition '%s' - no triggers (triggers are mandatory)", condition.Name)
			continue
		}

		glog.Infof("  Resolving condition '%s' (type=%s) with %d desired states",
			condition.Name, condition.Triggers[0].ConditionType, len(condition.DesiredStates))

		pinCommands, err := hcm.resolveDpllPinCommands(condition, clockChain)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to resolve DPLL pin commands for condition %s: %w", condition.Name, err)
		}
		glog.Infof("    Condition '%s': resolved %d DPLL commands", condition.Name, len(pinCommands))
		pinCommandsPerCondition[condition.Name] = pinCommands

		sysFSCommands, err := hcm.resolveSysFSCommands(condition, clockChain)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to resolve sysFS commands for condition %s: %w", condition.Name, err)
		}
		glog.Infof("    Condition '%s': resolved %d sysfs commands", condition.Name, len(sysFSCommands))
		sysFSCommandsPerCondition[condition.Name] = sysFSCommands
	}
	return pinCommandsPerCondition, sysFSCommandsPerCondition, nil
}

// resolveClockChainStructure inspects the structure section and produces static, hardware-specific
// defaults that should be applied regardless of runtime behavior, such as:
// - eSync and refSync configuration (via translation to the DPLL commands)
// - pin phase adjustments (factory/internal and internal/external from config)
// - default pin priorities resulting from factory defaults
// - sysfs commands when a pin is associated with a connector
//
// This function delegates to a hardware-specific definition based on Subsystem.HardwareSpecificDefinitions.
// Example: "intel/e810" would map to pkg/hardwareconfig/hardware-specific/intel/e810
// where YAML definitions convert static declarations into concrete commands.
func (hcm *HardwareConfigManager) resolveClockChainStructure(hwConfig ptpv2alpha1.HardwareConfig) ([]dpll.PinParentDeviceCtl, []SysFSCommand, error) {
	cc := hwConfig.Spec.Profile.ClockChain

	allPins := make([]dpll.PinParentDeviceCtl, 0)
	allSysfs := make([]SysFSCommand, 0)

	for _, subsystem := range cc.Structure {
		hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
		glog.Infof("  Subsystem %s: hardware definition='%s'", subsystem.Name, hwDefPath)
		if hwDefPath == "" {
			glog.Infof("  Subsystem %s has no hardware-specific definition; skipping", subsystem.Name)
			continue
		}

		pins, sysfs, err := hcm.resolveSubsystemStructure(hwDefPath, subsystem, cc)
		if err != nil {
			return nil, nil, fmt.Errorf("hardware-specific '%s' failed for subsystem %s: %w", hwDefPath, subsystem.Name, err)
		}
		glog.Infof("    Subsystem %s: resolved %d structure DPLL commands, %d sysfs commands", subsystem.Name, len(pins), len(sysfs))
		allPins = append(allPins, pins...)
		allSysfs = append(allSysfs, sysfs...)
	}

	return allPins, allSysfs, nil
}

// resolveSubsystemStructure dispatches to a hardware-specific definition implementation.
// Skeleton only: wire vendor-specific translators here.
func (hcm *HardwareConfigManager) resolveSubsystemStructure(hwDefPath string, subsystem ptpv2alpha1.Subsystem, clockChain *ptpv2alpha1.ClockChain) ([]dpll.PinParentDeviceCtl, []SysFSCommand, error) {
	// Load YAML defaults from pkg/hardwareconfig/hardware-specific/<hwDefPath>/defaults.yaml
	spec, err := hcm.getHardwareDefaults(hwDefPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load hardware defaults for '%s': %w", hwDefPath, err)
	}
	if spec == nil {
		glog.Infof("No hardware defaults found for '%s' (subsystem %s) - skipping", hwDefPath, subsystem.Name)
		return nil, nil, nil
	}

	pins := make([]dpll.PinParentDeviceCtl, 0)
	sysfs := make([]SysFSCommand, 0)

	// Translate pin default priorities/states (apply unconditionally)
	for label, overrides := range spec.PinDefaults {
		var eecPrio, ppsPrio *uint32
		if overrides.EEC != nil && overrides.EEC.Priority != nil {
			v := uint32(*overrides.EEC.Priority)
			eecPrio = &v
		}
		if overrides.PPS != nil && overrides.PPS.Priority != nil {
			v := uint32(*overrides.PPS.Priority)
			ppsPrio = &v
		}
		if cmd, ok := hcm.buildPinCommandFromDefaults(subsystem, hwDefPath, label, overrides, eecPrio, ppsPrio); ok {
			pins = append(pins, cmd)
		}
	}

	// Process eSync/frequency configuration for all pins in the subsystem
	esyncPins, err := hcm.buildESyncPinCommands(subsystem, hwDefPath, clockChain, spec)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build eSync pin commands: %w", err)
	}
	pins = append(pins, esyncPins...)

	// Build phase adjustment commands for pins with PhaseAdjustment configured
	phaseAdjustPins, err := hcm.buildPhaseAdjustmentCommands(subsystem, hwDefPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build phase adjustment commands: %w", err)
	}
	pins = append(pins, phaseAdjustPins...)

	// Translate connectorCommands to SysFS actions based on connectors referenced by pins
	inputs, outputs := collectConnectorUsage(subsystem)
	defaultInterface := firstInterface(subsystem)
	extraSysfs, err := hcm.translateConnectorCommands(spec.ConnectorCommands, defaultInterface, subsystem.Name, inputs, outputs)
	if err != nil {
		return nil, nil, err
	}
	sysfs = append(sysfs, extraSysfs...)

	return pins, sysfs, nil
}

// getHardwareDefaults returns cached hardware defaults for the given hwDefPath,
// loading them once if not present.
// Goroutine concurrency safety not needed here as the cache is only accessed by the main routine
func (hcm *HardwareConfigManager) getHardwareDefaults(hwDefPath string) (*HardwareDefaults, error) {
	if hwDefPath == "" {
		return nil, nil
	}
	spec, ok := hcm.hwDefaultsCache[hwDefPath]
	if ok {
		return spec, nil
	}

	// Load board label map if ConfigMap loader is available
	var labelMap BoardLabelMap
	if hcm.configMapLoader != nil {
		var err error
		labelMap, err = hcm.configMapLoader.LoadBoardLabelMap(hwDefPath)
		if err != nil {
			// Log but continue - remapping is optional (ConfigMap not found is not an error)
			glog.Warningf("Failed to load board label map for %s: %v (using embedded defaults)", hwDefPath, err)
		}
	}

	loaded, err := LoadHardwareDefaults(hwDefPath, labelMap)
	if err != nil {
		return nil, err
	}
	hcm.hwDefaultsCache[hwDefPath] = loaded
	return loaded, nil
}

// buildESyncPinCommands processes all pins in the subsystem and builds frequency/eSync commands
func (hcm *HardwareConfigManager) buildESyncPinCommands(subsystem ptpv2alpha1.Subsystem, hwDefPath string, clockChain *ptpv2alpha1.ClockChain, hwSpec *HardwareDefaults) ([]dpll.PinParentDeviceCtl, error) {
	if hcm.pinCache == nil {
		return nil, fmt.Errorf("hcm is not initialized for subsystem %s", subsystem.Name)
	}

	commands := make([]dpll.PinParentDeviceCtl, 0)

	// Resolve clock ID from network interface
	networkInterface := subsystem.DPLL.NetworkInterface
	if networkInterface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
		networkInterface = subsystem.Ethernet[0].Ports[0]
	}
	if networkInterface == "" {
		return nil, fmt.Errorf("no network interface found for subsystem %s", subsystem.Name)
	}

	clockID, err := hcm.getClockIDCached(networkInterface, hwDefPath)
	if err != nil {
		glog.Warningf("Failed to resolve clock ID for subsystem %s interface %s: %v", subsystem.Name, networkInterface, err)
		return nil, nil
	}

	// Process all pin types (inputs and outputs)
	pinGroups := []struct {
		pins    map[string]ptpv2alpha1.PinConfig
		isInput bool
		name    string
	}{
		{subsystem.DPLL.PhaseInputs, true, "phase inputs"},
		{subsystem.DPLL.PhaseOutputs, false, "phase outputs"},
		{subsystem.DPLL.FrequencyInputs, true, "frequency inputs"},
		{subsystem.DPLL.FrequencyOutputs, false, "frequency outputs"},
	}

	for _, group := range pinGroups {
		for boardLabel, pinCfg := range group.pins {
			cmds := hcm.buildPinFrequencyCommands(clockID, boardLabel, pinCfg, clockChain, hwSpec, group.isInput)
			commands = append(commands, cmds...)
		}
	}

	return commands, nil
}

// buildPhaseAdjustmentCommands builds DPLL commands for phase adjustments from populated PhaseAdjustment fields
func (hcm *HardwareConfigManager) buildPhaseAdjustmentCommands(subsystem ptpv2alpha1.Subsystem, hwDefPath string) ([]dpll.PinParentDeviceCtl, error) {
	if hcm.pinCache == nil {
		return nil, fmt.Errorf("pin cache not initialized for subsystem %s", subsystem.Name)
	}

	// Resolve clock ID from network interface
	networkInterface := subsystem.DPLL.NetworkInterface
	if networkInterface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
		networkInterface = subsystem.Ethernet[0].Ports[0]
	}
	if networkInterface == "" {
		return nil, fmt.Errorf("no network interface found for subsystem %s", subsystem.Name)
	}

	clockID, err := hcm.getClockIDCached(networkInterface, hwDefPath)
	if err != nil {
		glog.Warningf("Failed to resolve clock ID for subsystem %s interface %s: %v", subsystem.Name, networkInterface, err)
		return nil, nil
	}

	commands := make([]dpll.PinParentDeviceCtl, 0)

	// Process PhaseInputs and PhaseOutputs
	pinGroups := []struct {
		pins map[string]ptpv2alpha1.PinConfig
		name string
	}{
		{subsystem.DPLL.PhaseInputs, "phase inputs"},
		{subsystem.DPLL.PhaseOutputs, "phase outputs"},
	}

	for _, group := range pinGroups {
		for boardLabel, pinCfg := range group.pins {
			if pinCfg.PhaseAdjustment == nil {
				continue
			}

			// Get pin from cache
			pin, found := hcm.pinCache.GetPin(clockID, boardLabel)
			if !found {
				glog.V(2).Infof("Pin %s not found for clock %#x (phase adjustment)", boardLabel, clockID)
				continue
			}

			// PhaseAdjustment contains the total adjustment value:
			// (internal_adjustment + user_adjustment)
			// where:
			// - internal_adjustment = -internal_delay (delay from delays.yaml is negated)
			// - user_adjustment = user-provided value (already an adjustment, not negated)
			// This total is sent directly to DPLL without further negation
			totalAdjustment := *pinCfg.PhaseAdjustment

			// Convert to int32 and validate against pin limits
			adjustmentInt32 := int32(totalAdjustment)

			// Clamp to pin's min/max range
			if pin.PhaseAdjustMin != 0 && adjustmentInt32 < pin.PhaseAdjustMin {
				glog.Warningf("Pin %s phase adjustment %d ps clamped to minimum %d ps", boardLabel, adjustmentInt32, pin.PhaseAdjustMin)
				adjustmentInt32 = pin.PhaseAdjustMin
			}
			if pin.PhaseAdjustMax != 0 && adjustmentInt32 > pin.PhaseAdjustMax {
				glog.Warningf("Pin %s phase adjustment %d ps clamped to maximum %d ps", boardLabel, adjustmentInt32, pin.PhaseAdjustMax)
				adjustmentInt32 = pin.PhaseAdjustMax
			}

			// Round to granularity if specified
			if pin.PhaseAdjustGran > 1 {
				rounded := (adjustmentInt32 / int32(pin.PhaseAdjustGran)) * int32(pin.PhaseAdjustGran)
				if adjustmentInt32 != rounded {
					glog.V(3).Infof("Pin %s phase adjustment rounded from %d to %d ps (granularity: %d ps)",
						boardLabel, adjustmentInt32, rounded, pin.PhaseAdjustGran)
					adjustmentInt32 = rounded
				}
			}

			// Build command
			cmd := dpll.PinParentDeviceCtl{
				ID:          pin.ID,
				PhaseAdjust: &adjustmentInt32,
			}

			commands = append(commands, cmd)
			glog.Infof("Phase adjustment command for pin %s (id=%d): %d ps (total adjustment)",
				boardLabel, pin.ID, adjustmentInt32)
			glog.Infof("  Phase adjustment command details: %s", formatDpllPinCommand(cmd))
		}
	}

	return commands, nil
}

// buildPinFrequencyCommands builds DPLL commands to set pin frequency (with optional eSync)
// Returns a sequence of commands based on vendor-specific definitions
func (hcm *HardwareConfigManager) buildPinFrequencyCommands(clockID uint64, boardLabel string, pinCfg ptpv2alpha1.PinConfig, clockChain *ptpv2alpha1.ClockChain, hwSpec *HardwareDefaults, isInput bool) []dpll.PinParentDeviceCtl {
	pin, found := hcm.pinCache.GetPin(clockID, boardLabel)
	if !found {
		glog.V(2).Infof("Pin %s not found for clock %#x (eSync/frequency config)", boardLabel, clockID)
		return nil
	}

	// Resolve frequency, eSync, and duty cycle configuration
	frequency, esyncFreq, dutyCycle, hasConfig := hcm.resolvePinFrequency(pinCfg, clockChain)
	if !hasConfig {
		return nil
	}

	// If only frequency is set (no eSync), return a single command
	if esyncFreq == 0 {
		if frequency > 0 {
			cmd := dpll.PinParentDeviceCtl{ID: pin.ID, Frequency: &frequency}
			glog.Infof("Pin %s (id=%d, clock=%#x): frequency=%d Hz", boardLabel, pin.ID, clockID, frequency)
			return []dpll.PinParentDeviceCtl{cmd}
		}
		return nil
	}

	// eSync is configured - use vendor-specific command sequence
	glog.Infof("Pin %s (id=%d, clock=%#x): eSync frequency=%d Hz, duty cycle=%d%%, transfer frequency=%d Hz",
		boardLabel, pin.ID, clockID, esyncFreq, dutyCycle, frequency)

	// Get vendor-specific command sequence
	if hwSpec == nil || hwSpec.PinEsyncCommands == nil {
		// No vendor-specific sequence - fallback to single command
		glog.Warningf("No vendor-specific eSync sequence defined, using fallback for pin %s", boardLabel)
		cmd := dpll.PinParentDeviceCtl{
			ID:             pin.ID,
			Frequency:      &frequency,
			EsyncFrequency: &esyncFreq,
		}
		return []dpll.PinParentDeviceCtl{cmd}
	}

	// Select appropriate command sequence (input vs output)
	var cmdSequence []PinESyncCommand
	if isInput {
		cmdSequence = hwSpec.PinEsyncCommands.Inputs
	} else {
		cmdSequence = hwSpec.PinEsyncCommands.Outputs
	}

	if len(cmdSequence) == 0 {
		glog.Warningf("Empty eSync command sequence for pin %s (input=%v), using fallback", boardLabel, isInput)
		cmd := dpll.PinParentDeviceCtl{
			ID:             pin.ID,
			Frequency:      &frequency,
			EsyncFrequency: &esyncFreq,
		}
		return []dpll.PinParentDeviceCtl{cmd}
	}

	// Build command sequence
	commands := make([]dpll.PinParentDeviceCtl, 0, len(cmdSequence))
	for _, cmdDef := range cmdSequence {
		if cmdDef.Type != "DPLLWrite" {
			glog.Warningf("Unsupported eSync command type '%s' for pin %s, skipping", cmdDef.Type, boardLabel)
			continue
		}

		cmd := dpll.PinParentDeviceCtl{ID: pin.ID}

		// Set argument-based fields
		for _, arg := range cmdDef.Arguments {
			switch arg {
			case "frequency":
				cmd.Frequency = &frequency
			case "eSyncFrequency":
				cmd.EsyncFrequency = &esyncFreq
			default:
				glog.Warningf("Unknown argument '%s' in eSync command for pin %s", arg, boardLabel)
			}
		}

		// Set parent device states
		if len(cmdDef.PinParentDevices) > 0 {
			cmd.PinParentCtl = make([]dpll.PinControl, 0, len(cmdDef.PinParentDevices))
			for _, parentCfg := range cmdDef.PinParentDevices {
				// Find matching parent device in pin cache
				for _, parent := range pin.ParentDevice {
					// Match by device index: 0=EEC, 1=PPS
					deviceName := ""
					if parent.Direction == dpll.PinDirectionOutput {
						if len(cmd.PinParentCtl) == 0 {
							deviceName = "EEC"
						} else {
							deviceName = "PPS"
						}
					}

					if strings.EqualFold(deviceName, parentCfg.ParentDevice) {
						state, err := GetPinStateUint32(parentCfg.State)
						if err != nil {
							glog.Warningf("Invalid state '%s' for parent %s: %v", parentCfg.State, parentCfg.ParentDevice, err)
							continue
						}
						pc := dpll.PinControl{
							PinParentID: parent.ParentID,
							State:       &state,
						}
						cmd.PinParentCtl = append(cmd.PinParentCtl, pc)
					}
				}
			}
		}

		commands = append(commands, cmd)
		argStr := strings.Join(cmdDef.Arguments, ", ")
		if argStr == "" {
			argStr = "none"
		}
		glog.Infof("  eSync cmd[%d] for pin %s: %s (args=[%s], parents=%d)",
			len(commands), boardLabel, cmdDef.Description, argStr, len(cmd.PinParentCtl))
	}

	return commands
}

// resolvePinFrequency resolves the pin frequency configuration from PinConfig
// Returns: (transferFrequency, esyncFrequency, dutyCyclePct, hasConfig)
func (hcm *HardwareConfigManager) resolvePinFrequency(pinCfg ptpv2alpha1.PinConfig, clockChain *ptpv2alpha1.ClockChain) (uint64, uint64, int64, bool) {
	// If eSyncConfigName is set, resolve from commonDefinitions
	if pinCfg.ESyncConfigName != "" {
		if clockChain.CommonDefinitions == nil {
			glog.Warningf("eSync config '%s' referenced but no commonDefinitions", pinCfg.ESyncConfigName)
			return 0, 0, 0, false
		}

		for _, esyncDef := range clockChain.CommonDefinitions.ESyncDefinitions {
			if esyncDef.Name == pinCfg.ESyncConfigName {
				transferFreq := uint64(esyncDef.ESyncConfig.TransferFrequency)
				esyncFreq := uint64(esyncDef.ESyncConfig.EmbeddedSyncFrequency)
				dutyCycle := esyncDef.ESyncConfig.DutyCyclePct

				// Apply defaults
				if esyncFreq == 0 {
					esyncFreq = 1 // Default to 1Hz if not specified
				}
				if dutyCycle == 0 {
					dutyCycle = 25 // Default to 25% if not specified
				}

				glog.Infof("Resolved eSync config '%s': transfer=%d Hz, esync=%d Hz, duty=%d%%",
					esyncDef.Name, transferFreq, esyncFreq, dutyCycle)
				return transferFreq, esyncFreq, dutyCycle, true
			}
		}
		glog.Warningf("eSync config '%s' not found in commonDefinitions", pinCfg.ESyncConfigName)
		return 0, 0, 0, false
	}

	// If frequency is directly specified, use it (no eSync, no duty cycle)
	if pinCfg.Frequency != nil && *pinCfg.Frequency > 0 {
		return uint64(*pinCfg.Frequency), 0, 0, true
	}

	// No frequency configuration
	return 0, 0, 0, false
}

// buildPinPriorityCommand searches the pin cache by clock ID and board label and creates a priority command.
func (hcm *HardwareConfigManager) buildPinPriorityCommand(clockID uint64, boardLabel string, eecPrio, ppsPrio *uint32) (dpll.PinParentDeviceCtl, bool) {
	if hcm.pinCache == nil {
		return dpll.PinParentDeviceCtl{}, false
	}
	pin, found := hcm.pinCache.GetPin(clockID, boardLabel)
	if !found {
		return dpll.PinParentDeviceCtl{}, false
	}
	cmd := dpll.PinParentDeviceCtl{ID: pin.ID, PinParentCtl: make([]dpll.PinControl, 0)}
	for idx, parent := range pin.ParentDevice {
		pc := dpll.PinControl{PinParentID: parent.ParentID}
		if parent.Direction == dpll.PinDirectionInput {
			// Map device index 0->EEC, 1->PPS as in legacy implementation
			if idx == 0 && eecPrio != nil {
				pc.Prio = eecPrio
			} else if idx == 1 && ppsPrio != nil {
				pc.Prio = ppsPrio
			}
		}
		cmd.PinParentCtl = append(cmd.PinParentCtl, pc)
	}
	return cmd, true
}

func (hcm *HardwareConfigManager) buildPinCommandFromDefaults(subsystem ptpv2alpha1.Subsystem, hwDefPath string, label string, overrides *PinDefault, eecPrio, ppsPrio *uint32) (dpll.PinParentDeviceCtl, bool) {
	// Resolve clock ID from network interface
	networkInterface := subsystem.DPLL.NetworkInterface
	if networkInterface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
		networkInterface = subsystem.Ethernet[0].Ports[0]
	}
	if networkInterface == "" {
		glog.Warningf("hardware defaults: no network interface found for subsystem %s", subsystem.Name)
		return dpll.PinParentDeviceCtl{}, false
	}

	clockID, err := hcm.getClockIDCached(networkInterface, hwDefPath)
	if err != nil {
		glog.Warningf("hardware defaults: failed to resolve clock ID for subsystem %s interface %s: %v", subsystem.Name, networkInterface, err)
		return dpll.PinParentDeviceCtl{}, false
	}

	cmd, found := hcm.buildPinPriorityCommand(clockID, label, eecPrio, ppsPrio)
	if !found {
		glog.Warningf("hardware defaults: pin %s not found for clock %x", label, clockID)
		return dpll.PinParentDeviceCtl{}, false
	}

	// Apply state overrides if provided
	if overrides != nil {
		pin, ok := hcm.pinCache.GetPin(clockID, label)
		if ok {
			for idx := range cmd.PinParentCtl {
				parent := cmd.PinParentCtl[idx]
				if idx < len(pin.ParentDevice) && pin.ParentDevice[idx].Direction == dpll.PinDirectionOutput {
					if overrides.EEC != nil && overrides.EEC.State != "" {
						if stateVal, stateErr := GetPinStateUint32(overrides.EEC.State); stateErr == nil {
							parent.State = &stateVal
						}
					}
					if overrides.PPS != nil && overrides.PPS.State != "" {
						if stateVal, stateErr := GetPinStateUint32(overrides.PPS.State); stateErr == nil {
							parent.State = &stateVal
						}
					}
					cmd.PinParentCtl[idx] = parent
				}
			}
		}
	}

	return cmd, true
}

func (hcm *HardwareConfigManager) resolveDpllPinCommands(condition ptpv2alpha1.Condition, clockChain *ptpv2alpha1.ClockChain) ([]dpll.PinParentDeviceCtl, error) {
	pinCommands := []dpll.PinParentDeviceCtl{}
	for idx, desiredState := range condition.DesiredStates {
		if desiredState.DPLL != nil {
			glog.Infof("      DesiredState[%d]: DPLL boardLabel=%s subsystem=%s", idx, desiredState.DPLL.BoardLabel, desiredState.DPLL.Subsystem)
			pinCommand, err := hcm.createPinCommandForDPLLDesiredState(*desiredState.DPLL, clockChain)
			if err != nil {
				return nil, fmt.Errorf("failed to create pin command for DPLL desired state: %w", err)
			}
			pinCommands = append(pinCommands, pinCommand)
		} else {
			glog.Infof("      DesiredState[%d]: no DPLL section", idx)
		}
	}
	return pinCommands, nil
}

// processPTPPinConfig processes a PTP pin configuration and returns sysFS commands.
func (hcm *HardwareConfigManager) processPTPPinConfig(pin ptpv2alpha1.PTPPinDesiredState, clockChain *ptpv2alpha1.ClockChain) ([]SysFSCommand, error) {
	paths, err := hcm.resolvePTPPinPath(pin, clockChain)
	if err != nil {
		return nil, err
	}
	value := serializePTPPinValue(pin)
	commands := make([]SysFSCommand, len(paths))
	for i, path := range paths {
		commands[i] = SysFSCommand{
			Path:        path,
			Value:       value,
			Description: pin.Description,
		}
	}
	return commands, nil
}

// processPTPPeriodConfig processes a PTP period configuration and returns sysFS commands.
func (hcm *HardwareConfigManager) processPTPPeriodConfig(period ptpv2alpha1.PTPPeriodDesiredState, clockChain *ptpv2alpha1.ClockChain) ([]SysFSCommand, error) {
	paths, err := hcm.resolvePTPPeriodPath(period, clockChain)
	if err != nil {
		return nil, err
	}
	value := serializePTPPeriodValue(period)
	commands := make([]SysFSCommand, len(paths))
	for i, path := range paths {
		commands[i] = SysFSCommand{
			Path:        path,
			Value:       value,
			Description: period.Description,
		}
	}
	return commands, nil
}

func (hcm *HardwareConfigManager) resolveSysFSCommands(condition ptpv2alpha1.Condition, clockChain *ptpv2alpha1.ClockChain) ([]SysFSCommand, error) {
	var sysFSCommands []SysFSCommand
	for idx, desiredState := range condition.DesiredStates {
		if desiredState.PTPPin != nil {
			glog.Infof("      DesiredState[%d]: ptpPin name=%s func=%s chan=%d sourceName=%s", idx, desiredState.PTPPin.Name, desiredState.PTPPin.Func.String(), desiredState.PTPPin.Chan, desiredState.PTPPin.SourceName)
			commands, err := hcm.processPTPPinConfig(*desiredState.PTPPin, clockChain)
			if err != nil {
				glog.Errorf("      DesiredState[%d]: failed to resolve PTP pin path: %v", idx, err)
				return nil, fmt.Errorf("failed to resolve PTP pin path (DesiredState[%d]): %w", idx, err)
			}
			glog.Infof("        Resolved to %d paths", len(commands))
			sysFSCommands = append(sysFSCommands, commands...)
		}
		if desiredState.PTPPeriod != nil {
			glog.Infof("      DesiredState[%d]: ptpPeriod index=%d sourceName=%s", idx, desiredState.PTPPeriod.Index, desiredState.PTPPeriod.SourceName)
			commands, err := hcm.processPTPPeriodConfig(*desiredState.PTPPeriod, clockChain)
			if err != nil {
				glog.Errorf("      DesiredState[%d]: failed to resolve PTP period path: %v", idx, err)
				return nil, fmt.Errorf("failed to resolve PTP period path (DesiredState[%d]): %w", idx, err)
			}
			glog.Infof("        Resolved to %d paths", len(commands))
			sysFSCommands = append(sysFSCommands, commands...)
		}
	}
	return sysFSCommands, nil
}

func (hcm *HardwareConfigManager) createPinCommandForDPLLDesiredState(dpllDesiredState ptpv2alpha1.DPLLDesiredState, clockChain *ptpv2alpha1.ClockChain) (dpll.PinParentDeviceCtl, error) {
	// Resolve clock ID from subsystem
	if dpllDesiredState.Subsystem == "" {
		return dpll.PinParentDeviceCtl{}, fmt.Errorf("subsystem not specified in DPLL desired state")
	}

	networkInterface, err := GetSubsystemNetworkInterface(clockChain, dpllDesiredState.Subsystem)
	if err != nil {
		return dpll.PinParentDeviceCtl{}, fmt.Errorf("failed to get network interface for subsystem %s: %w", dpllDesiredState.Subsystem, err)
	}

	hwDefPath, hasHwDef, hwDefErr := getSubsystemHardwareDefinition(clockChain, dpllDesiredState.Subsystem)
	if hwDefErr != nil {
		return dpll.PinParentDeviceCtl{}, fmt.Errorf("failed to resolve hardware definition for subsystem %s: %w", dpllDesiredState.Subsystem, hwDefErr)
	}
	if !hasHwDef {
		glog.V(3).Infof("Subsystem %s has no hardware-specific definitions; using fallback clock ID transformer", dpllDesiredState.Subsystem)
	}

	clockID, err := hcm.getClockIDCached(networkInterface, hwDefPath)
	if err != nil {
		return dpll.PinParentDeviceCtl{}, fmt.Errorf("failed to resolve clock ID for subsystem %s interface %s: %w", dpllDesiredState.Subsystem, networkInterface, err)
	}

	pin, found := hcm.pinCache.GetPin(clockID, dpllDesiredState.BoardLabel)
	if !found {
		return dpll.PinParentDeviceCtl{}, fmt.Errorf("DPLL pin not found in cache (subsystem=%s clock=%#x label=%s)", dpllDesiredState.Subsystem, clockID, dpllDesiredState.BoardLabel)
	}

	pinCommand := dpll.PinParentDeviceCtl{
		ID:           pin.ID,
		PinParentCtl: make([]dpll.PinControl, 0),
	}

	for _, parentDevice := range pin.ParentDevice {
		pc := dpll.PinControl{
			PinParentID: parentDevice.ParentID,
		}

		isEEC := parentDevice.ParentID&0x1 == 0 // EEC parent device has even ParentID
		isPPS := parentDevice.ParentID&0x1 == 1 // PPS parent device has odd ParentID

		if parentDevice.Direction == dpll.PinDirectionInput {
			// Input pins support both priority AND state changes
			// Apply EEC settings only to EEC parent device
			if isEEC && dpllDesiredState.EEC != nil {
				if dpllDesiredState.EEC.Priority != nil {
					priority := uint32(*dpllDesiredState.EEC.Priority)
					pc.Prio = &priority
				}
				if dpllDesiredState.EEC.State != "" {
					state, stateErr := GetPinStateUint32(dpllDesiredState.EEC.State)
					if stateErr != nil {
						return dpll.PinParentDeviceCtl{}, fmt.Errorf("invalid EEC state: %w", stateErr)
					}
					pc.State = &state
				}
			}
			// Apply PPS settings only to PPS parent device
			if isPPS && dpllDesiredState.PPS != nil {
				if dpllDesiredState.PPS.Priority != nil {
					priority := uint32(*dpllDesiredState.PPS.Priority)
					pc.Prio = &priority
				}
				if dpllDesiredState.PPS.State != "" {
					state, stateErr := GetPinStateUint32(dpllDesiredState.PPS.State)
					if stateErr != nil {
						return dpll.PinParentDeviceCtl{}, fmt.Errorf("invalid PPS state: %w", stateErr)
					}
					pc.State = &state
				}
			}
		} else {
			// Output pins can only have state changed (no priority)
			// Apply EEC settings only to EEC parent device
			if isEEC && dpllDesiredState.EEC != nil && dpllDesiredState.EEC.State != "" {
				state, stateErr := GetPinStateUint32(dpllDesiredState.EEC.State)
				if stateErr != nil {
					return dpll.PinParentDeviceCtl{}, fmt.Errorf("invalid EEC state: %w", stateErr)
				}
				pc.State = &state
			}
			// Apply PPS settings only to PPS parent device
			if isPPS && dpllDesiredState.PPS != nil && dpllDesiredState.PPS.State != "" {
				state, stateErr := GetPinStateUint32(dpllDesiredState.PPS.State)
				if stateErr != nil {
					return dpll.PinParentDeviceCtl{}, fmt.Errorf("invalid PPS state: %w", stateErr)
				}
				pc.State = &state
			}
		}

		pinCommand.PinParentCtl = append(pinCommand.PinParentCtl, pc)
	}

	return pinCommand, nil
}

// applyDefaultAndInitConditions extracts and applies "default" and "init" conditions in order
func (hcm *HardwareConfigManager) applyDefaultAndInitConditions(clockChain *ptpv2alpha1.ClockChain, profileName string, enrichedConfig *enrichedHardwareConfig) error {
	if clockChain.Behavior == nil {
		glog.Infof("No behavior section found in hardware profile %s", profileName)
		return nil
	}

	// Extract conditions by type - for default/init, we need to check all sources
	// since sourceName is mandatory in triggers
	var defaultConditions []ptpv2alpha1.Condition
	var initConditions []ptpv2alpha1.Condition
	seenDefault := make(map[string]bool)
	seenInit := make(map[string]bool)

	// For each source, extract matching default/init conditions
	for _, source := range clockChain.Behavior.Sources {
		for _, condition := range clockChain.Behavior.Conditions {
			// Triggers are mandatory - skip conditions without triggers
			if len(condition.Triggers) == 0 {
				continue
			}
			for _, trigger := range condition.Triggers {
				if trigger.ConditionType == ConditionTypeDefault && trigger.SourceName == source.Name {
					if !seenDefault[condition.Name] {
						defaultConditions = append(defaultConditions, condition)
						seenDefault[condition.Name] = true
					}
				}
				if trigger.ConditionType == ConditionTypeInit && trigger.SourceName == source.Name {
					if !seenInit[condition.Name] {
						initConditions = append(initConditions, condition)
						seenInit[condition.Name] = true
					}
				}
			}
		}
	}

	glog.Infof("Found %d default conditions and %d init conditions in profile %s",
		len(defaultConditions), len(initConditions), profileName)

	// Apply default conditions first
	for i, condition := range defaultConditions {
		glog.Infof("Applying default condition %d: %s", i+1, condition.Name)
		if err := hcm.applyDesiredStatesInOrder(condition, profileName, enrichedConfig.Spec.Profile.ClockChain); err != nil {
			return fmt.Errorf("failed to apply default condition '%s': %w", condition.Name, err)
		}
	}

	// Apply init conditions second
	for i, condition := range initConditions {
		glog.Infof("Applying init condition %d: %s", i+1, condition.Name)
		if err := hcm.applyDesiredStatesInOrder(condition, profileName, enrichedConfig.Spec.Profile.ClockChain); err != nil {
			return fmt.Errorf("failed to apply init condition '%s': %w", condition.Name, err)
		}
	}

	return nil
}

// extractConditionsByType extracts conditions that have triggers matching the condition type
// Triggers are mandatory - conditions must have at least one trigger
func (hcm *HardwareConfigManager) extractConditionsByType(conditions []ptpv2alpha1.Condition, conditionType string) []ptpv2alpha1.Condition {
	var matchingConditions []ptpv2alpha1.Condition

	for _, condition := range conditions {
		// Check if any trigger in this condition matches the desired type
		for _, trigger := range condition.Triggers {
			if trigger.ConditionType == conditionType {
				matchingConditions = append(matchingConditions, condition)
				break // Found matching type, add condition and move to next
			}
		}
	}

	return matchingConditions
}

// extractConditionsByTypeAndSource extracts conditions that have triggers matching both condition type and source name
// sourceName is mandatory and must match exactly
// Triggers are mandatory - conditions must have at least one trigger
func (hcm *HardwareConfigManager) extractConditionsByTypeAndSource(conditions []ptpv2alpha1.Condition, conditionType string, sourceName string) []ptpv2alpha1.Condition {
	var matchingConditions []ptpv2alpha1.Condition

	for _, condition := range conditions {
		// Check if any trigger in this condition matches the desired type and source
		for _, trigger := range condition.Triggers {
			if trigger.ConditionType == conditionType && trigger.SourceName == sourceName {
				matchingConditions = append(matchingConditions, condition)
				break // Found matching type and source, add condition and move to next
			}
		}
	}

	return matchingConditions
}

// applyPTPCommands applies a list of sysFS commands by writing values to paths.
func (hcm *HardwareConfigManager) applyPTPCommands(commands []SysFSCommand, idx int, configType string) error {
	paths := make([]string, len(commands))
	for i, cmd := range commands {
		paths[i] = cmd.Path
	}
	glog.V(4).Infof("  DesiredState[%d]: resolved %s paths=%v", idx, configType, paths)
	for _, cmd := range commands {
		if err := hcm.writeSysFSValue(cmd.Path, cmd.Value); err != nil {
			return fmt.Errorf("desiredState[%d] %s write failed: %w", idx, configType, err)
		}
	}
	return nil
}

// applyDesiredStatesInOrder resolves and applies commands exactly in the order defined in DesiredStates.
func (hcm *HardwareConfigManager) applyDesiredStatesInOrder(condition ptpv2alpha1.Condition, profileName string, clockChain *ptpv2alpha1.ClockChain) error {
	glog.Infof("Applying %d desired states for condition '%s' in profile %s", len(condition.DesiredStates), condition.Name, profileName)
	for idx, desiredState := range condition.DesiredStates {
		if desiredState.PTPPin != nil {
			glog.Infof("  DesiredState[%d]: applying PTP pin name=%s func=%s chan=%d", idx, desiredState.PTPPin.Name, desiredState.PTPPin.Func.String(), desiredState.PTPPin.Chan)
			commands, err := hcm.processPTPPinConfig(*desiredState.PTPPin, clockChain)
			if err != nil {
				return fmt.Errorf("desiredState[%d] ptp pin resolve failed: %w", idx, err)
			}
			if err = hcm.applyPTPCommands(commands, idx, "ptp pin"); err != nil {
				return err
			}
		}
		if desiredState.PTPPeriod != nil {
			glog.Infof("  DesiredState[%d]: applying PTP period index=%d", idx, desiredState.PTPPeriod.Index)
			commands, err := hcm.processPTPPeriodConfig(*desiredState.PTPPeriod, clockChain)
			if err != nil {
				return fmt.Errorf("desiredState[%d] ptp period resolve failed: %w", idx, err)
			}
			if err = hcm.applyPTPCommands(commands, idx, "ptp period"); err != nil {
				return err
			}
		}
		if desiredState.DPLL != nil {
			glog.Infof("  DesiredState[%d]: applying DPLL boardLabel=%s subsystem=%s", idx, desiredState.DPLL.BoardLabel, desiredState.DPLL.Subsystem)
			cmd, err := hcm.createPinCommandForDPLLDesiredState(*desiredState.DPLL, clockChain)
			if err != nil {
				return fmt.Errorf("desiredState[%d] dpll command build failed: %w", idx, err)
			}
			if err = hcm.pinApplier([]dpll.PinParentDeviceCtl{cmd}); err != nil {
				return fmt.Errorf("desiredState[%d] dpll apply failed: %w", idx, err)
			}
		}
	}
	return nil
}

// applyStructureDefaults extracts and applies static, hardware-specific defaults for a given hardware config
func (hcm *HardwareConfigManager) applyStructureDefaults(enrichedConfig *enrichedHardwareConfig, profileName string) error {
	if len(enrichedConfig.structureSysFSCommands) > 0 {
		if err := hcm.applyCachedSysFSCommands("structure-defaults", profileName, enrichedConfig.structureSysFSCommands); err != nil {
			return fmt.Errorf("failed to apply structure sysFS defaults: %w", err)
		}
	}

	if len(enrichedConfig.structurePinCommands) > 0 {
		if err := hcm.applyDpllPinCommands(profileName, "structure-defaults", enrichedConfig.structurePinCommands); err != nil {
			return fmt.Errorf("failed to apply structure DPLL defaults: %w", err)
		}
	}

	for _, subsystem := range enrichedConfig.Spec.Profile.ClockChain.Structure {
		glog.Infof("  Subsystem: %s (Hardware: %s, Network Interface: %s)",
			subsystem.Name, subsystem.HardwareSpecificDefinitions, subsystem.DPLL.NetworkInterface)
	}

	return nil
}

func (hcm *HardwareConfigManager) applyBehaviorConditions(enrichedConfig *enrichedHardwareConfig, profileName string) error {
	return hcm.applyDefaultAndInitConditions(enrichedConfig.Spec.Profile.ClockChain, profileName, enrichedConfig)
}

func (hcm *HardwareConfigManager) applyCachedSysFSCommands(conditionName, profileName string, commands []SysFSCommand) error {
	glog.Infof("  Applying %d sysFS commands for '%s' in profile %s", len(commands), conditionName, profileName)

	for i, cmd := range commands {
		glog.Infof("    sysfs[%d]: path=%s value=%s desc=%s", i+1, cmd.Path, cmd.Value, cmd.Description)
		if err := hcm.writeSysFSValue(cmd.Path, cmd.Value); err != nil {
			return fmt.Errorf("failed to write sysfs value '%s' to '%s': %w", cmd.Value, cmd.Path, err)
		}
	}

	return nil
}

func (hcm *HardwareConfigManager) applyDpllPinCommands(profileName, context string, commands []dpll.PinParentDeviceCtl) error {
	if len(commands) == 0 {
		glog.Infof("  No DPLL pin commands for %s in profile %s", context, profileName)
		return nil
	}

	glog.Infof("  Applying %d DPLL pin commands for %s in profile %s", len(commands), context, profileName)
	for i, c := range commands {
		glog.Infof("    pin[%d]: %s", i+1, formatDpllPinCommand(c))
	}

	// Merge commands for the same pin ID to avoid overwriting fields
	mergedCommands := mergePinCommandsByID(commands)
	if len(mergedCommands) != len(commands) {
		glog.Infof("  Merged %d commands into %d commands (by pin ID)", len(commands), len(mergedCommands))
		for i, c := range mergedCommands {
			glog.Infof("    merged[%d]: %s", i+1, formatDpllPinCommand(c))
		}
	}

	if err := hcm.pinApplier(mergedCommands); err != nil {
		return fmt.Errorf("dpll pin apply (%s): %w", context, err)
	}

	return nil
}

// mergePinCommandsByID merges multiple commands for the same pin ID into a single command
// Fields from later commands take precedence, but all non-nil fields are preserved
func mergePinCommandsByID(commands []dpll.PinParentDeviceCtl) []dpll.PinParentDeviceCtl {
	if len(commands) == 0 {
		return commands
	}

	// Map pin ID -> merged command
	merged := make(map[uint32]*dpll.PinParentDeviceCtl)

	for i := range commands {
		cmd := &commands[i]
		existing, exists := merged[cmd.ID]
		if !exists {
			// First command for this pin ID - create a copy
			mergedCmd := *cmd
			// Deep copy slices and pointers
			if cmd.PinParentCtl != nil {
				mergedCmd.PinParentCtl = make([]dpll.PinControl, len(cmd.PinParentCtl))
				copy(mergedCmd.PinParentCtl, cmd.PinParentCtl)
			}
			if cmd.Frequency != nil {
				freq := *cmd.Frequency
				mergedCmd.Frequency = &freq
			}
			if cmd.PhaseAdjust != nil {
				phase := *cmd.PhaseAdjust
				mergedCmd.PhaseAdjust = &phase
			}
			if cmd.EsyncFrequency != nil {
				esync := *cmd.EsyncFrequency
				mergedCmd.EsyncFrequency = &esync
			}
			merged[cmd.ID] = &mergedCmd
			continue
		}

		// Merge with existing command - later commands take precedence for individual fields
		// but we merge PinParentCtl slices
		if cmd.Frequency != nil {
			freq := *cmd.Frequency
			existing.Frequency = &freq
		}
		if cmd.PhaseAdjust != nil {
			phase := *cmd.PhaseAdjust
			existing.PhaseAdjust = &phase
		}
		if cmd.EsyncFrequency != nil {
			esync := *cmd.EsyncFrequency
			existing.EsyncFrequency = &esync
		}

		// Merge PinParentCtl - combine parent controls, with later ones taking precedence for same parent ID
		if len(cmd.PinParentCtl) > 0 {
			if existing.PinParentCtl == nil {
				existing.PinParentCtl = make([]dpll.PinControl, 0, len(cmd.PinParentCtl))
			}
			// Create a map of existing parent controls by parent ID
			parentMap := make(map[uint32]*dpll.PinControl)
			for j := range existing.PinParentCtl {
				parentMap[existing.PinParentCtl[j].PinParentID] = &existing.PinParentCtl[j]
			}
			// Add or update parent controls from new command
			for j := range cmd.PinParentCtl {
				pc := cmd.PinParentCtl[j]
				if existingPC, found := parentMap[pc.PinParentID]; found {
					// Update existing parent control
					if pc.State != nil {
						state := *pc.State
						existingPC.State = &state
					}
					if pc.Prio != nil {
						prio := *pc.Prio
						existingPC.Prio = &prio
					}
					if pc.Direction != nil {
						direction := *pc.Direction
						existingPC.Direction = &direction
					}
				} else {
					// Add new parent control
					newPC := dpll.PinControl{PinParentID: pc.PinParentID}
					if pc.State != nil {
						state := *pc.State
						newPC.State = &state
					}
					if pc.Prio != nil {
						prio := *pc.Prio
						newPC.Prio = &prio
					}
					if pc.Direction != nil {
						direction := *pc.Direction
						newPC.Direction = &direction
					}
					existing.PinParentCtl = append(existing.PinParentCtl, newPC)
					parentMap[pc.PinParentID] = &existing.PinParentCtl[len(existing.PinParentCtl)-1]
				}
			}
		}
	}

	// Convert map to slice
	result := make([]dpll.PinParentDeviceCtl, 0, len(merged))
	for _, cmd := range merged {
		result = append(result, *cmd)
	}

	return result
}

func (hcm *HardwareConfigManager) resolveSysFSPtpDevice(interfacePath string) ([]string, error) {
	glog.V(4).Infof("resolveSysFSPtpDevice: resolving %s", interfacePath)
	return ptpDeviceResolver(interfacePath)
}

// resolvePTPPinPath resolves the sysFS path for a PTP pin configuration.
// Path pattern: /sys/class/net/{interface}/device/ptp/ptp*/pins/{pinName}
func (hcm *HardwareConfigManager) resolvePTPPinPath(pin ptpv2alpha1.PTPPinDesiredState, clockChain *ptpv2alpha1.ClockChain) ([]string, error) {
	interfaceName, err := hcm.getInterfaceNameFromSources(pin.SourceName, clockChain)
	if err != nil {
		return nil, fmt.Errorf("failed to get interface names: %w", err)
	}

	if interfaceName == nil {
		return nil, fmt.Errorf("no interface names found for path templating")
	}

	// Path pattern: /sys/class/net/{interface}/device/ptp/ptp*/pins/{pinName}
	basePath := fmt.Sprintf("/sys/class/net/%s/device/ptp/ptp*/pins/%s", *interfaceName, pin.Name)
	return hcm.resolveSysFSPtpDevice(basePath)
}

// resolvePTPPeriodPath resolves the sysFS path for a PTP period configuration.
// Path pattern: /sys/class/net/{interface}/device/ptp/ptp*/period
func (hcm *HardwareConfigManager) resolvePTPPeriodPath(period ptpv2alpha1.PTPPeriodDesiredState, clockChain *ptpv2alpha1.ClockChain) ([]string, error) {
	interfaceName, err := hcm.getInterfaceNameFromSources(period.SourceName, clockChain)
	if err != nil {
		return nil, fmt.Errorf("failed to get interface names: %w", err)
	}

	if interfaceName == nil {
		return nil, fmt.Errorf("no interface names found for path templating")
	}

	// Path pattern: /sys/class/net/{interface}/device/ptp/ptp*/period
	basePath := fmt.Sprintf("/sys/class/net/%s/device/ptp/ptp*/period", *interfaceName)
	return hcm.resolveSysFSPtpDevice(basePath)
}

// serializePTPPinValue converts PTPPinDesiredState to sysFS format: "<func> <chan>"
func serializePTPPinValue(pin ptpv2alpha1.PTPPinDesiredState) string {
	return fmt.Sprintf("%d %d", int64(pin.Func), pin.Chan)
}

// serializePTPPeriodValue converts PTPPeriodDesiredState to sysFS format: "<index> <start.sec> <start.nsec> <period.sec> <period.nsec>"
// Defaults start and period to {sec: 0, nsec: 0} if omitted.
func serializePTPPeriodValue(period ptpv2alpha1.PTPPeriodDesiredState) string {
	start := period.Start
	if start == nil {
		start = &ptpv2alpha1.PTPTimeSpec{Sec: 0, Nsec: 0}
	}
	periodDuration := period.Period
	if periodDuration == nil {
		periodDuration = &ptpv2alpha1.PTPTimeSpec{Sec: 0, Nsec: 0}
	}
	return fmt.Sprintf("%d %d %d %d %d",
		period.Index,
		start.Sec,
		start.Nsec,
		periodDuration.Sec,
		periodDuration.Nsec)
}

// getInterfaceNameFromSources retrieves the network interface name for a given source.
// For DPLL sources (dpllPhaseLocked), it uses the subsystem's network interface from the structure section.
// For PTP sources (ptpTimeReceiver), it finds the matching port in the source's specified subsystem.
// Both source types use the source.Subsystem field to find the correct subsystem (not just the first one).
func (hcm *HardwareConfigManager) getInterfaceNameFromSources(sourceName string, clockChain *ptpv2alpha1.ClockChain) (*string, error) {
	if clockChain.Behavior == nil {
		return nil, fmt.Errorf("no behavior section found in clock chain")
	}

	// Find the named source (or first PTP source if sourceName is empty)
	var source *ptpv2alpha1.SourceConfig
	if sourceName == "" {
		// Empty sourceName means find the first PTP source (used for leadingInterface)
		for i := range clockChain.Behavior.Sources {
			if clockChain.Behavior.Sources[i].SourceType == ptpTimeReceiverType && len(clockChain.Behavior.Sources[i].PTPTimeReceivers) > 0 {
				source = &clockChain.Behavior.Sources[i]
				break
			}
		}
		if source == nil {
			return nil, fmt.Errorf("no ptpTimeReceiver source found")
		}
	} else {
		// Find the named source
		for i := range clockChain.Behavior.Sources {
			if clockChain.Behavior.Sources[i].Name == sourceName {
				source = &clockChain.Behavior.Sources[i]
				break
			}
		}
		if source == nil {
			return nil, fmt.Errorf("source %s not found", sourceName)
		}
	}

	// For DPLL sources, use the subsystem's network interface from structure
	if source.SourceType == "dpllPhaseLocked" {
		if source.Subsystem == "" {
			return nil, fmt.Errorf("DPLL source %s has no subsystem specified", sourceName)
		}
		// Find the subsystem in the structure section
		for _, subsystem := range clockChain.Structure {
			if subsystem.Name == source.Subsystem {
				// Get network interface from DPLL.NetworkInterface or fall back to first Ethernet port
				networkInterface := subsystem.DPLL.NetworkInterface
				if networkInterface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
					networkInterface = subsystem.Ethernet[0].Ports[0]
				}
				if networkInterface == "" {
					return nil, fmt.Errorf("subsystem %s has no network interface", source.Subsystem)
				}
				return &networkInterface, nil
			}
		}
		return nil, fmt.Errorf("subsystem %s not found in structure section", source.Subsystem)
	}

	// For PTP sources, use the subsystem's default interface (not the ptpTimeReceiver port)
	// The ptpTimeReceiver is used to identify the subsystem, but the default interface is used for sysfs paths
	if len(source.PTPTimeReceivers) == 0 {
		return nil, fmt.Errorf("source %s has no ptpTimeReceivers", sourceName)
	}
	if source.Subsystem == "" {
		return nil, fmt.Errorf("PTP source %s has no subsystem specified", sourceName)
	}

	// Find the subsystem specified in the source
	for _, subsystem := range clockChain.Structure {
		if subsystem.Name == source.Subsystem {
			// Verify that the ptpTimeReceiver port exists in this subsystem (for validation)
			upstreamPort := source.PTPTimeReceivers[0]
			portFound := false
			for _, eth := range subsystem.Ethernet {
				for _, port := range eth.Ports {
					if port == upstreamPort {
						portFound = true
						break
					}
				}
				if portFound {
					break
				}
			}
			// Also check if it matches the network interface
			if !portFound && subsystem.DPLL.NetworkInterface == upstreamPort {
				portFound = true
			}
			if !portFound {
				return nil, fmt.Errorf("ptpTimeReceiver port %s not found in subsystem %s", upstreamPort, source.Subsystem)
			}

			// Return the subsystem's default interface (DPLL.NetworkInterface or first Ethernet port)
			networkInterface := subsystem.DPLL.NetworkInterface
			if networkInterface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
				networkInterface = subsystem.Ethernet[0].Ports[0]
			}
			if networkInterface == "" {
				return nil, fmt.Errorf("subsystem %s has no network interface", source.Subsystem)
			}
			return &networkInterface, nil
		}
	}

	return nil, fmt.Errorf("subsystem %s not found in structure section for source %s", source.Subsystem, sourceName)
}

// writeSysFSValue writes a value to a sysFS path
func (hcm *HardwareConfigManager) writeSysFSValue(path, value string) error {
	return hcm.sysfsWriter(path, value)
}

func (hcm *HardwareConfigManager) overrideExecutors(pin func([]dpll.PinParentDeviceCtl) error, sysfs func(string, string) error) {
	if pin != nil {
		hcm.pinApplier = pin
	}
	if sysfs != nil {
		hcm.sysfsWriter = sysfs
	}
}

func (hcm *HardwareConfigManager) resetExecutors() {
	hcm.pinApplier = func(cmds []dpll.PinParentDeviceCtl) error { return BatchPinSet(&cmds) }
	hcm.sysfsWriter = func(path, value string) error { return os.WriteFile(path, []byte(value), 0o644) }
}

// ApplyConditionForProfile applies cached commands for a specific condition (e.g., "locked", "lost") for a PTP profile
func (hcm *HardwareConfigManager) ApplyConditionForProfile(nodeProfile *ptpv1.PtpProfile, conditionType string) error {
	if nodeProfile.Name == nil {
		return fmt.Errorf("PTP profile has no name")
	}

	// Find enriched hardware configs for this profile
	var relevantConfigs []enrichedHardwareConfig
	for _, hwConfig := range hcm.hardwareConfigs {
		if hwConfig.Spec.RelatedPtpProfileName == *nodeProfile.Name {
			relevantConfigs = append(relevantConfigs, hwConfig)
		}
	}

	if len(relevantConfigs) == 0 {
		glog.Infof("No hardware configurations found for PTP profile %s condition %s", *nodeProfile.Name, conditionType)
		return nil
	}

	glog.Infof("Applying condition '%s' in %d hardware configurations for PTP profile %s",
		conditionType, len(relevantConfigs), *nodeProfile.Name)

	for _, enrichedConfig := range relevantConfigs {
		profileName := defaultProfileName
		if enrichedConfig.Spec.Profile.Name != nil {
			profileName = *enrichedConfig.Spec.Profile.Name
		}

		// Apply commands strictly in the order defined in DesiredStates for the given condition type
		// Use cached commands when available (all conditions are cached during enrichment)
		if enrichedConfig.Spec.Profile.ClockChain.Behavior != nil {
			conditions := hcm.extractConditionsByType(enrichedConfig.Spec.Profile.ClockChain.Behavior.Conditions, conditionType)
			for _, condition := range conditions {
				// Try to use cached commands first
				cachedSysFS, hasSysFS := enrichedConfig.sysFSCommands[condition.Name]
				cachedDPLL, hasDPLL := enrichedConfig.dpllPinCommands[condition.Name]

				if hasSysFS || hasDPLL {
					glog.V(3).Infof("Using cached commands for condition '%s'", condition.Name)
					// Apply cached sysfs commands first (if any)
					if hasSysFS && len(cachedSysFS) > 0 {
						if err := hcm.applyCachedSysFSCommands(condition.Name, profileName, cachedSysFS); err != nil {
							return fmt.Errorf("failed to apply cached sysfs commands for condition '%s': %w", condition.Name, err)
						}
					}
					// Apply cached DPLL commands (if any)
					if hasDPLL && len(cachedDPLL) > 0 {
						if err := hcm.applyDpllPinCommands(profileName, condition.Name, cachedDPLL); err != nil {
							return fmt.Errorf("failed to apply cached DPLL commands for condition '%s': %w", condition.Name, err)
						}
					}
				} else {
					// Fall back to resolving on-the-fly if not cached (shouldn't happen in normal operation)
					glog.Warningf("No cached commands found for condition '%s', resolving on-the-fly", condition.Name)
					if err := hcm.applyDesiredStatesInOrder(condition, profileName, enrichedConfig.Spec.Profile.ClockChain); err != nil {
						return fmt.Errorf("failed to apply condition '%s' in order: %w", condition.Name, err)
					}
				}
			}
		}
	}

	return nil
}

// ProcessDPLLDeviceNotifications processes DPLL device notifications and triggers hardwareconfig conditions
// for sources with sourceType: dpllPhaseLocked when devices enter LOCKED_HO_ACQ state.
// All matching logic (clock ID, lock status, source matching) happens here.
func (hcm *HardwareConfigManager) ProcessDPLLDeviceNotifications(devices []*dpll.DoDeviceGetReply) error {
	if len(devices) == 0 {
		return nil
	}

	hcm.mu.RLock()
	defer hcm.mu.RUnlock()

	// Process each device notification
	for _, device := range devices {
		// Check if device is in LOCKED_HO_ACQ state
		// LockStatus is uint32, DPLL_LOCKED_HO_ACQ = 3
		// In nlUpdateState, reply.LockStatus is compared directly to DPLL constants
		if device.LockStatus != uint32(dpllcfg.DPLL_LOCKED_HO_ACQ) {
			continue
		}

		clockID := device.ClockID
		glog.Infof("Processing DPLL device notification: clockID=%#x, LockStatus=LOCKED_HO_ACQ", clockID)

		// Find matching hardwareconfigs by clock ID
		for _, hwConfig := range hcm.hardwareConfigs {
			// Check each subsystem to see if it matches this clock ID
			for _, subsystem := range hwConfig.Spec.Profile.ClockChain.Structure {
				// Resolve clock ID for this subsystem
				networkInterface := subsystem.DPLL.NetworkInterface
				if networkInterface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
					networkInterface = subsystem.Ethernet[0].Ports[0]
				}
				if networkInterface == "" {
					continue
				}

				hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
				subsystemClockID, err := hcm.getClockIDCached(networkInterface, hwDefPath)
				if err != nil {
					glog.V(3).Infof("Failed to resolve clock ID for subsystem %s interface %s: %v", subsystem.Name, networkInterface, err)
					continue
				}

				// If clock IDs match, find sources with dpllPhaseLocked in this subsystem
				if subsystemClockID == clockID {
					glog.V(3).Infof("Clock ID match found for subsystem %s (clockID=%#x)", subsystem.Name, clockID)
					if hwConfig.Spec.Profile.ClockChain.Behavior == nil {
						glog.V(3).Infof("No behavior section found in hardware config %s", hwConfig.Name)
						continue
					}

					glog.Infof("Checking %d sources in hardware config %s for dpllPhaseLocked type",
						len(hwConfig.Spec.Profile.ClockChain.Behavior.Sources), hwConfig.Name)
					// Find sources with sourceType: dpllPhaseLocked and matching subsystem
					for _, source := range hwConfig.Spec.Profile.ClockChain.Behavior.Sources {
						glog.Infof("Checking source '%s': sourceType=%s, subsystem=%s (looking for subsystem=%s, type=dpllPhaseLocked)",
							source.Name, source.SourceType, source.Subsystem, subsystem.Name)
						if source.SourceType == "dpllPhaseLocked" && source.Subsystem == subsystem.Name {
							glog.Infof("Found matching source '%s' (subsystem=%s, clockID=%#x), triggering 'locked' condition",
								source.Name, subsystem.Name, clockID)

							// Trigger "locked" condition for this source
							// We need to apply conditions for the related PTP profile
							profileName := hwConfig.Spec.RelatedPtpProfileName
							if profileName == "" {
								glog.Warningf("Hardwareconfig %s has no relatedPtpProfileName, skipping condition trigger", hwConfig.Name)
								continue
							}

							// Find conditions with "locked" type that reference this source
							if hwConfig.Spec.Profile.ClockChain.Behavior.Conditions != nil {
								glog.Infof("Extracting 'locked' conditions for source '%s' from %d total conditions",
									source.Name, len(hwConfig.Spec.Profile.ClockChain.Behavior.Conditions))
								conditions := hcm.extractConditionsByTypeAndSource(hwConfig.Spec.Profile.ClockChain.Behavior.Conditions, ConditionTypeLocked, source.Name)
								glog.Infof("Found %d matching 'locked' conditions for source '%s'", len(conditions), source.Name)
								if len(conditions) == 0 {
									glog.Warningf("No 'locked' conditions found for source '%s' - ensure a condition exists with trigger sourceName='%s' and conditionType='locked'",
										source.Name, source.Name)
								}
								for _, condition := range conditions {
									hwProfileName := "unnamed"
									if hwConfig.Spec.Profile.Name != nil {
										hwProfileName = *hwConfig.Spec.Profile.Name
									}
									glog.Infof("Applying 'locked' condition '%s' for source '%s' (profile=%s, hwProfile=%s)",
										condition.Name, source.Name, profileName, hwProfileName)
									if err = hcm.applyDesiredStatesInOrder(condition, hwProfileName, hwConfig.Spec.Profile.ClockChain); err != nil {
										glog.Errorf("Failed to apply condition '%s' for source '%s': %v", condition.Name, source.Name, err)
										// Continue with other conditions even if one fails
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return nil
}

// GetHardwareConfigCount returns the number of hardware configs currently managed
func (hcm *HardwareConfigManager) GetHardwareConfigCount() int {
	return len(hcm.hardwareConfigs)
}

// ClearHardwareConfigs clears all hardware configurations
func (hcm *HardwareConfigManager) ClearHardwareConfigs() {
	hcm.hardwareConfigs = make([]enrichedHardwareConfig, 0)
}

// GetPTPStateDetector returns a PTP state detector for processing PTP events
// This allows external components to use the hardwareconfig-based PTP processing
func (hcm *HardwareConfigManager) GetPTPStateDetector() *PTPStateDetector {
	// Create a new PTPStateDetector with the current hardware configs
	return NewPTPStateDetector(hcm)
}

func (hcm *HardwareConfigManager) setHardwareConfigs(hwConfigs []enrichedHardwareConfig) {
	hcm.mu.Lock()
	defer hcm.mu.Unlock()
	hcm.hardwareConfigs = hwConfigs
	if !hcm.ready {
		hcm.ready = true
		if hcm.cond != nil {
			hcm.cond.Broadcast()
		}
	}
}

// WaitForHardwareConfigs waits for hardware configurations to be ready within the specified timeout
func (hcm *HardwareConfigManager) WaitForHardwareConfigs(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	hcm.mu.Lock()
	defer hcm.mu.Unlock()
	for !hcm.ready {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		if hcm.cond == nil {
			return hcm.ready
		}
		hcm.cond.Wait()
	}
	return true
}

// HasReadyHardwareConfigs returns true if hardware configurations are ready
func (hcm *HardwareConfigManager) HasReadyHardwareConfigs() bool {
	hcm.mu.RLock()
	defer hcm.mu.RUnlock()
	return hcm.ready
}

// HasHardwareConfigs returns true if any hardware configurations are loaded
func (hcm *HardwareConfigManager) HasHardwareConfigs() bool {
	hcm.mu.RLock()
	defer hcm.mu.RUnlock()
	return len(hcm.hardwareConfigs) > 0
}

// ReadyHardwareConfigForProfile returns true if hardware configurations are ready for the specified profile
func (hcm *HardwareConfigManager) ReadyHardwareConfigForProfile(name string) bool {
	hcm.mu.RLock()
	defer hcm.mu.RUnlock()
	if !hcm.ready {
		return false
	}
	for _, hw := range hcm.hardwareConfigs {
		if hw.Spec.RelatedPtpProfileName == name {
			return true
		}
	}
	return false
}

// detectRemovedHardwareConfigs detects which hardwareconfigs were removed by comparing
// current configs with new configs. Returns a list of removed enriched configs.
func (hcm *HardwareConfigManager) detectRemovedHardwareConfigs(newConfigs []ptpv2alpha1.HardwareConfig) []enrichedHardwareConfig {
	hcm.mu.RLock()
	defer hcm.mu.RUnlock()

	// Build a map of new config names for quick lookup
	newConfigMap := make(map[string]bool)
	for _, cfg := range newConfigs {
		newConfigMap[cfg.Name] = true
	}

	// Find configs that exist in current but not in new
	var removed []enrichedHardwareConfig
	for _, currentConfig := range hcm.hardwareConfigs {
		if !newConfigMap[currentConfig.Name] {
			removed = append(removed, currentConfig)
			glog.Infof("Hardware config '%s' (profile: %s) was removed", currentConfig.Name, currentConfig.Spec.RelatedPtpProfileName)
		}
	}

	return removed
}

// applyVendorDefaultsForRemovedConfigs applies vendor defaults for removed hardwareconfigs
// to reset DPLL to default state when hardwareconfig is removed.
func (hcm *HardwareConfigManager) applyVendorDefaultsForRemovedConfigs(removedConfigs []enrichedHardwareConfig) error {
	if hcm.pinCache == nil {
		var err error
		hcm.pinCache, err = GetDpllPins()
		if err != nil {
			return fmt.Errorf("failed to get DPLL pins for vendor defaults: %w", err)
		}
	}

	for _, removedConfig := range removedConfigs {
		profileName := defaultProfileName
		if removedConfig.Spec.Profile.Name != nil {
			profileName = *removedConfig.Spec.Profile.Name
		}

		glog.Infof("Applying vendor defaults for removed hardware config '%s' (profile: %s)", removedConfig.Name, removedConfig.Spec.RelatedPtpProfileName)

		// Apply vendor defaults for each subsystem in the removed config
		for _, subsystem := range removedConfig.Spec.Profile.ClockChain.Structure {
			hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
			if hwDefPath == "" {
				glog.V(3).Infof("Subsystem %s has no hardware-specific definitions, skipping vendor defaults", subsystem.Name)
				continue
			}

			// Resolve vendor defaults for this subsystem (same logic as resolveSubsystemStructure)
			pins, sysfs, err := hcm.resolveSubsystemStructure(hwDefPath, subsystem, removedConfig.Spec.Profile.ClockChain)
			if err != nil {
				glog.Errorf("Failed to resolve vendor defaults for subsystem %s (hwDefPath: %s): %v", subsystem.Name, hwDefPath, err)
				continue
			}

			// Apply DPLL pin commands (vendor defaults)
			if len(pins) > 0 {
				glog.Infof("Applying %d vendor default DPLL pin commands for removed subsystem %s", len(pins), subsystem.Name)
				if err = hcm.applyDpllPinCommands(profileName, "vendor-defaults-removed", pins); err != nil {
					glog.Errorf("Failed to apply vendor default DPLL commands for subsystem %s: %v", subsystem.Name, err)
					// Continue with other subsystems
				}
			}

			// Apply sysfs commands (vendor defaults)
			if len(sysfs) > 0 {
				glog.Infof("Applying %d vendor default sysfs commands for removed subsystem %s", len(sysfs), subsystem.Name)
				if err = hcm.applyCachedSysFSCommands("vendor-defaults-removed", profileName, sysfs); err != nil {
					glog.Errorf("Failed to apply vendor default sysfs commands for subsystem %s: %v", subsystem.Name, err)
					// Continue with other subsystems
				}
			}
		}
	}

	return nil
}

// extractHoldoverParameters extracts holdover parameters from all subsystems in the clock chain
func (hcm *HardwareConfigManager) extractHoldoverParameters(hwConfig ptpv2alpha1.HardwareConfig) map[uint64]*ptpv2alpha1.HoldoverParameters {
	params := make(map[uint64]*ptpv2alpha1.HoldoverParameters)

	for _, subsystem := range hwConfig.Spec.Profile.ClockChain.Structure {
		if subsystem.DPLL.HoldoverParameters != nil {
			// Resolve clock ID from network interface
			networkInterface := subsystem.DPLL.NetworkInterface
			if networkInterface == "" && len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
				networkInterface = subsystem.Ethernet[0].Ports[0]
			}
			if networkInterface == "" {
				glog.Warningf("Subsystem %s has holdover parameters but no network interface", subsystem.Name)
				continue
			}

			hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
			if hwDefPath == "" {
				glog.V(3).Infof("Subsystem %s has holdover parameters but no hardware-specific definitions; using fallback transformer", subsystem.Name)
			}

			clockID, err := hcm.getClockIDCached(networkInterface, hwDefPath)
			if err != nil {
				glog.Warningf("Subsystem %s: failed to resolve clock ID from interface %s: %v", subsystem.Name, networkInterface, err)
				continue
			}

			// Apply defaults if values are not specified
			hoParams := *subsystem.DPLL.HoldoverParameters // Copy the struct
			if hoParams.MaxInSpecOffset == 0 {
				hoParams.MaxInSpecOffset = 100 // Default: 100ns
			}
			if hoParams.LocalMaxHoldoverOffset == 0 {
				hoParams.LocalMaxHoldoverOffset = 1500 // Default: 1500ns
			}
			if hoParams.LocalHoldoverTimeout == 0 {
				hoParams.LocalHoldoverTimeout = 14400 // Default: 14400s (4 hours)
			}

			params[clockID] = &hoParams
			glog.Infof("  Holdover params for clock %#x (subsystem %s): MaxInSpec=%dns, LocalMaxOffset=%dns, Timeout=%ds",
				clockID, subsystem.Name,
				hoParams.MaxInSpecOffset,
				hoParams.LocalMaxHoldoverOffset,
				hoParams.LocalHoldoverTimeout)
		}
	}

	return params
}

// GetHoldoverParameters returns holdover parameters for a specific clock ID from the given profile
// Returns nil if no holdover parameters are configured for the clock ID
func (hcm *HardwareConfigManager) GetHoldoverParameters(profileName string, clockID uint64) *ptpv2alpha1.HoldoverParameters {
	hcm.mu.RLock()
	defer hcm.mu.RUnlock()

	for _, hwConfig := range hcm.hardwareConfigs {
		if hwConfig.Spec.RelatedPtpProfileName == profileName {
			if params, found := hwConfig.holdoverParams[clockID]; found {
				return params
			}
		}
	}

	return nil
}
