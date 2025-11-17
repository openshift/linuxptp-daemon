package intel

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
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
	EnableDefaultConfig bool                         `json:"enableDefaultConfig"`
	DevicePins          map[string]map[string]string `json:"pins"`
	DpllSettings        map[string]uint64            `json:"settings"`
	PhaseOffsetPins     map[string]map[string]string `json:"phaseOffsetPins"`
	PhaseInputs         []PhaseInputs                `json:"interconnections"`
}

// GetPhaseInputs implements PhaseInputsProvider
func (o E830Opts) GetPhaseInputs() []PhaseInputs { return o.PhaseInputs }

// E830PluginData is the plugin data for e830 plugin
type E830PluginData struct {
	hwplugins *[]string
}

// OnPTPConfigChangeE830 is called on PTP config change for e830 plugin
func OnPTPConfigChangeE830(_ *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	glog.Info("calling onPTPConfigChange for e830 plugin")
	var opts E830Opts
	var err error
	var optsByteArray []byte
	for name, raw := range (*nodeProfile).Plugins {
		if name == pluginNameE830 {
			optsByteArray, _ = json.Marshal(raw)
			err = json.Unmarshal(optsByteArray, &opts)
			if err != nil {
				glog.Error("e830 failed to unmarshal opts: " + err.Error())
			}
			if (*nodeProfile).PtpSettings == nil {
				(*nodeProfile).PtpSettings = make(map[string]string)
			}
			iceClockID, iceErr := getClockIDByModule("ice")
			if iceErr != nil {
				glog.Errorf("e830: failed to resolve ICE DPLL clock ID via netlink: %v", iceErr)
			}
			for device, pins := range opts.DevicePins {
				dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				if iceErr == nil {
					(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(iceClockID, 10)
				}
				for pin, value := range pins {
					deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", device)
					phcs, pErr := os.ReadDir(deviceDir)
					if pErr != nil {
						glog.Error("e830 failed to read " + deviceDir + ": " + pErr.Error())
						continue
					}
					for _, phc := range phcs {
						pinPath := fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/pins/%s", device, phc.Name(), pin)
						glog.Infof("echo %s > %s", value, pinPath)
						err = os.WriteFile(pinPath, []byte(value), 0666)
						if err != nil {
							glog.Error("e830 failed to write " + value + " to " + pinPath + ": " + err.Error())
						}
					}
				}
			}
			for k, v := range opts.DpllSettings {
				if _, ok := (*nodeProfile).PtpSettings[k]; !ok {
					(*nodeProfile).PtpSettings[k] = strconv.FormatUint(v, 10)
				}
			}
			for iface, properties := range opts.PhaseOffsetPins {
				ifaceFound := false
				for dev := range opts.DevicePins {
					if strings.Compare(iface, dev) == 0 {
						ifaceFound = true
						break
					}
				}
				if !ifaceFound {
					glog.Errorf("e830 phase offset pin filter initialization failed: interface %s not found among  %v",
						iface, reflect.ValueOf(opts.DevicePins).MapKeys())
					break
				}
				for pinProperty, value := range properties {
					var clockIDUsed uint64
					if iceErr == nil {
						clockIDUsed = iceClockID
					}
					key := strings.Join([]string{iface, "phaseOffsetFilter", strconv.FormatUint(clockIDUsed, 10), pinProperty}, ".")
					(*nodeProfile).PtpSettings[key] = value
				}
			}
			if opts.PhaseInputs != nil {
				chain, ierr := InitClockChain(opts, nodeProfile)
				if ierr != nil {
					return ierr
				}
				(*nodeProfile).PtpSettings["leadingInterface"] = chain.LeadingNIC.Name
				(*nodeProfile).PtpSettings["upstreamPort"] = chain.LeadingNIC.UpstreamPort
			} else {
				glog.Error("no clock chain set")
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
	_plugin := plugin.Plugin{Name: pluginNameE830,
		OnPTPConfigChange:  OnPTPConfigChangeE830,
		AfterRunPTPCommand: AfterRunPTPCommandE830,
		PopulateHwConfig:   PopulateHwConfigE830,
	}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}
