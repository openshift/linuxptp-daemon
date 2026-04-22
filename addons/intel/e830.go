package intel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	dpll_netlink "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

var pluginNameE830 = "e830"

// E830Opts is the options for e830 plugin
type E830Opts struct {
	PluginOpts
}

// E830PluginData is the plugin data for e830 plugin
type E830PluginData struct {
	PluginData
}

func _hasDpllForClockID(clockID uint64) bool {
	if clockID == 0 {
		return false
	}
	conn, err := dpll_netlink.Dial(nil)
	if err != nil {
		glog.Warningf("failed to dial DPLL: %s", err)
		return false
	}
	defer conn.Close()

	devices, err := conn.DumpDeviceGet()
	if err != nil || devices == nil {
		glog.Infof("No DPLLs found on this system")
		return false
	}
	found := false
	for _, device := range devices {
		if device.ClockID == clockID {
			glog.Infof("Detected %s DPLL for clock ID %#x", dpll_netlink.GetDpllType(device.Type), clockID)
			found = true
		}
	}
	if !found {
		glog.Infof("No DPLL detected for clock ID %#x", clockID)
	}
	return found
}

// Function pointer for mocking
var hasDpllForClockID = _hasDpllForClockID

// OnPTPConfigChangeE830 is called on PTP config change for e830 plugin
func OnPTPConfigChangeE830(_ *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	glog.Infof("calling onPTPConfigChange for e830 plugin (%s)", *nodeProfile.Name)
	var opts E830Opts
	var err error
	var optsByteArray []byte

	if (*nodeProfile).PtpSettings == nil {
		(*nodeProfile).PtpSettings = make(map[string]string)
	}

	for name, raw := range (*nodeProfile).Plugins {
		if name == pluginNameE830 {
			// Parse user-specified config
			optsByteArray, _ = json.Marshal(raw)

			// Validate configuration before applying
			if validationErrors := ValidateE830Opts(optsByteArray); len(validationErrors) > 0 {
				return fmt.Errorf("e830 plugin configuration errors: %s", strings.Join(validationErrors, "; "))
			}

			err = json.Unmarshal(optsByteArray, &opts)
			if err != nil {
				return fmt.Errorf("e830 failed to unmarshal opts: %w", err)
			}

			allDevices := opts.allDevices()
			glog.Infof("Initializing e830 plugin for profile %s and devices %v", *nodeProfile.Name, allDevices)

			// Setup clockID (prefer ice modue clock ID for e830)
			clockIDs := make(map[string]uint64)
			for _, device := range allDevices {
				clockID := getClockID(device)
				clockIDs[device] = clockID
				dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				nodeProfile.PtpSettings[dpllClockIDStr] = strconv.FormatUint(clockID, 10)
				glog.Infof("e830: Detected %s=%d (%x)", dpllClockIDStr, clockID, clockID)
			}

			// Initialize all user-specified PGT pins and frequencies
			for device, pins := range opts.DevicePins {
				err = pinConfig.applyPinSet(device, pins)
				if err != nil {
					glog.Errorf("e830 failed to set Pin configuration for %s: %s", device, err)
				}
			}
			for device, frequencies := range opts.DeviceFreqencies {
				err = pinConfig.applyPinFrq(device, frequencies)
				if err != nil {
					glog.Errorf("e830 failed to set PHC frequencies for %s: %s", device, err)
				}
			}

			// Copy DPLL Settings from plugin config to PtpSettings
			for k, v := range opts.DpllSettings {
				if _, ok := (*nodeProfile).PtpSettings[k]; !ok {
					(*nodeProfile).PtpSettings[k] = strconv.FormatUint(v, 10)
				}
			}

			// Copy PhaseOffsetPins settings from plugin config to PtpSettings
			for iface, properties := range opts.PhaseOffsetPins {
				dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, iface)
				clockIDStr := nodeProfile.PtpSettings[dpllClockIDStr]
				for pinProperty, value := range properties {
					key := strings.Join([]string{iface, "phaseOffsetFilter", clockIDStr, pinProperty}, ".")
					(*nodeProfile).PtpSettings[key] = value
				}
			}

			// Setup BC configuration
			if tbcConfigured(nodeProfile) {
				if _, ok := nodeProfile.PtpSettings["upstreamPort"]; !ok {
					return errors.New("GNR-D T-BC must set upstreamPort")
				}
				if _, ok := nodeProfile.PtpSettings["leadingInterface"]; !ok {
					// TODO: We could actually figure this out based on upstreamPort... And the fact that there's only one NAC per GNR-D
					return errors.New("GNR-D T-BC must set leadingInterface")
				}
			}

			// Setup DPLL flags for all e830 cards
			for _, device := range allDevices {
				if hasDpllForClockID(clockIDs[device]) {
					// CF DPLL only provides PhaseStatus, not Phase Offset or Frequency Status:
					nodeProfile.PtpSettings[dpll.PtpSettingsDpllFlagsKey(device)] = strconv.FormatUint(uint64(dpll.FlagOnlyPhaseStatus), 10)
				} else {
					// No DPLL found: Mark this device to be ignored
					nodeProfile.PtpSettings[dpll.PtpSettingsDpllIgnoreKey(device)] = "true"
				}
			}
		}
	}
	return nil
}

// AfterRunPTPCommandE830 is called after running ptp command for e830 plugin
func AfterRunPTPCommandE830(_ *interface{}, _ *ptpv1.PtpProfile, _ string) error { return nil }

// E830 initializes the e830 plugin
func E830(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginNameE830 {
		glog.Errorf("Plugin must be initialized as 'e830'")
		return nil, nil
	}
	glog.Infof("registering e830 plugin")
	pluginData := E830PluginData{
		PluginData: PluginData{name: pluginNameE830},
	}
	_plugin := plugin.Plugin{
		Name:               pluginNameE830,
		OnPTPConfigChange:  OnPTPConfigChangeE830,
		AfterRunPTPCommand: AfterRunPTPCommandE830,
		PopulateHwConfig:   pluginData.PopulateHwConfig,
	}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
