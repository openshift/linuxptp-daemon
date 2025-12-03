package intel

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

var pluginNameE830 = "e830"

// E830Opts is the options for e830 plugin
type E830Opts struct {
	Devices          []string                     `json:"devices"`
	DevicePins       map[string]pinSet            `json:"pins"`
	DeviceFreqencies map[string]frqSet            `json:"frequencies"`
	DpllSettings     map[string]uint64            `json:"settings"`
	PhaseOffsetPins  map[string]map[string]string `json:"phaseOffsetPins"`
}

// allDevices enumerates all defined devices (Devices/DevicePins/DeviceFrequencies/PhaseOffsets)
func (opts *E830Opts) allDevices() []string {
	allDevices := opts.Devices
	allDevices = extendWithKeys(allDevices, opts.DevicePins)
	allDevices = extendWithKeys(allDevices, opts.DeviceFreqencies)
	allDevices = extendWithKeys(allDevices, opts.PhaseOffsetPins)
	return allDevices
}

// E830PluginData is the plugin data for e830 plugin
type E830PluginData struct {
	hwplugins *[]string
}

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
			err = json.Unmarshal(optsByteArray, &opts)
			if err != nil {
				glog.Error("e830 failed to unmarshal opts: " + err.Error())
			}

			allDevices := opts.allDevices()
			glog.Infof("Initializing e830 plugin for profile %s and devices %v", *nodeProfile.Name, allDevices)

			// Setup clockID (prefer ice modue clock ID for e830)
			iceClockID, iceErr := getClockIDByModule("ice")
			if iceErr != nil {
				glog.Errorf("e830: failed to resolve ICE DPLL clock ID via netlink: %v", iceErr)
			}
			for _, device := range allDevices {
				dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				if iceErr == nil {
					(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(iceClockID, 10)
					glog.Infof("Detected %s=%d (%x)", dpllClockIDStr, iceClockID, iceClockID)
				} else {
					glog.Errorf("No clockID detected for %s", device)
				}
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
				for pinProperty, value := range properties {
					var clockIDUsed uint64
					if iceErr == nil {
						clockIDUsed = iceClockID
					}
					key := strings.Join([]string{iface, "phaseOffsetFilter", strconv.FormatUint(clockIDUsed, 10), pinProperty}, ".")
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
				// e830 DPLL is inaccessible to software, so ensure the daemon ignores all e830 DPLLs for now:
				for _, device := range allDevices {
					// Note: Only set to "true" if it's unset; This allows overriding this by explicitly setting dpll.$iface.ignore = "false" in the PtpConfig section
					key := dpll.PtpSettingsDpllIgnoreKey(device)
					if value, ok := nodeProfile.PtpSettings[key]; ok {
						glog.Infof("Not setting %s (already \"%s\")", key, value)
					} else {
						nodeProfile.PtpSettings[key] = "true"
						glog.Infof("Setting %s = \"true\"", key)
					}
				}
			}
		}
	}
	return nil
}

// AfterRunPTPCommandE830 is called after running ptp command for e830 plugin
func AfterRunPTPCommandE830(_ *interface{}, _ *ptpv1.PtpProfile, _ string) error { return nil }

// PopulateHwConfigE830 populates hwconfig for e830 plugin
func PopulateHwConfigE830(_ *interface{}, _ *[]ptpv1.HwConfig) error { return nil }

// E830 initializes the e830 plugin
func E830(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginNameE830 {
		glog.Errorf("Plugin must be initialized as 'e830'")
		return nil, nil
	}
	glog.Infof("registering e830 plugin")
	hwplugins := []string{}
	pluginData := E830PluginData{hwplugins: &hwplugins}
	_plugin := plugin.Plugin{
		Name:               pluginNameE830,
		OnPTPConfigChange:  OnPTPConfigChangeE830,
		AfterRunPTPCommand: AfterRunPTPCommandE830,
		PopulateHwConfig:   PopulateHwConfigE830,
	}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
