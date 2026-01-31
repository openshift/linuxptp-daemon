package intel

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	dpll_netlink "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

var pluginNameE810 = "e810"

type E810Opts struct {
	EnableDefaultConfig bool                         `json:"enableDefaultConfig"`
	UblxCmds            UblxCmdList                  `json:"ublxCmds"`
	Devices             []string                     `json:"devices"`
	DevicePins          map[string]pinSet            `json:"pins"`
	DeviceFreqencies    map[string]frqSet            `json:"frequencies"`
	DpllSettings        map[string]uint64            `json:"settings"`
	PhaseOffsetPins     map[string]map[string]string `json:"phaseOffsetPins"`
	PhaseInputs         []PhaseInputs                `json:"interconnections"`
}

// allDevices enumerates all defined devices (Devices/DevicePins/DeviceFrequencies/PhaseOffsets)
func (opts *E810Opts) allDevices() []string {
	// Enumerate all defined devices (Devices/DevicePins/DeviceFrequencies)
	allDevices := opts.Devices
	allDevices = extendWithKeys(allDevices, opts.DevicePins)
	allDevices = extendWithKeys(allDevices, opts.DeviceFreqencies)
	allDevices = extendWithKeys(allDevices, opts.PhaseOffsetPins)
	return allDevices
}

// GetPhaseInputs implements PhaseInputsProvider
func (o E810Opts) GetPhaseInputs() []PhaseInputs { return o.PhaseInputs }

type E810UblxCmds struct {
	ReportOutput bool     `json:"reportOutput"`
	Args         []string `json:"args"`
}

type E810PluginData struct {
	hwplugins *[]string
}

// Sourced from https://github.com/RHsyseng/oot-ice/blob/main/ptp-config.sh
var EnableE810PTPConfig = `
#!/bin/bash
set -eu

ETH=$(grep -e 000e -e 000f /sys/class/net/*/device/subsystem_device | awk -F"/" '{print $5}')

for DEV in $ETH; do
  if [ -f /sys/class/net/$DEV/device/ptp/ptp*/pins/U.FL2 ]; then
    echo 0 2 > /sys/class/net/$DEV/device/ptp/ptp*/pins/U.FL2
    echo 0 1 > /sys/class/net/$DEV/device/ptp/ptp*/pins/U.FL1
    echo 0 2 > /sys/class/net/$DEV/device/ptp/ptp*/pins/SMA2
    echo 0 1 > /sys/class/net/$DEV/device/ptp/ptp*/pins/SMA1
  fi
done

echo "Disabled all SMA and U.FL Connections"
`

var (
	unitTest   bool
	clockChain ClockChainInterface = &ClockChain{}
)

// For mocking DPLL pin info
var DpllPins = []*dpll_netlink.PinInfo{}

func OnPTPConfigChangeE810(data *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	glog.Info("calling onPTPConfigChange for e810 plugin")
	var e810Opts E810Opts
	var err error

	e810Opts.EnableDefaultConfig = false

	for name, opts := range (*nodeProfile).Plugins {
		if name == pluginNameE810 {
			optsByteArray, _ := json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &e810Opts)
			if err != nil {
				glog.Error("e810 failed to unmarshal opts: " + err.Error())
			}
			// for unit testing only, PtpSettings may include "unitTest" key. The value is
			// the path where resulting configuration files will be written, instead of /var/run
			_, unitTest = (*nodeProfile).PtpSettings["unitTest"]
			if unitTest {
				MockPins()
			}

			allDevices := e810Opts.allDevices()
			glog.Infof("Initializing e810 plugin for profile %s and devices %v", *nodeProfile.Name, allDevices)

			if e810Opts.EnableDefaultConfig {
				stdout, _ := exec.Command("/usr/bin/bash", "-c", EnableE810PTPConfig).Output()
				glog.Infof(string(stdout))
			}
			if (*nodeProfile).PtpSettings == nil {
				(*nodeProfile).PtpSettings = make(map[string]string)
			}

			// Setup clockID
			for _, device := range allDevices {
				dpllClockIdStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				(*nodeProfile).PtpSettings[dpllClockIdStr] = strconv.FormatUint(getPCIClockID(device), 10)
			}

			// Initialize all user-specified phc pins and frequencies
			for device, pins := range e810Opts.DevicePins {
				err = pinConfig.applyPinSet(device, pins)
				if err != nil {
					glog.Errorf("e825 failed to set Pin configuration for %s: %s", device, err)
				}
			}
			for device, frequencies := range e810Opts.DeviceFreqencies {
				err = pinConfig.applyPinFrq(device, frequencies)
				if err != nil {
					glog.Errorf("e825 failed to set PHC frequencies for %s: %s", device, err)
				}
			}

			// Copy DPLL Settings from plugin config to PtpSettings
			for k, v := range e810Opts.DpllSettings {
				if _, ok := (*nodeProfile).PtpSettings[k]; !ok {
					(*nodeProfile).PtpSettings[k] = strconv.FormatUint(v, 10)
				}
			}

			// Copy PhaseOffsetPins settings from plugin config to PtpSettings
			for iface, properties := range e810Opts.PhaseOffsetPins {
				for pinProperty, value := range properties {
					key := strings.Join([]string{iface, "phaseOffsetFilter", strconv.FormatUint(getPCIClockID(iface), 10), pinProperty}, ".")
					(*nodeProfile).PtpSettings[key] = value
				}
			}

			// Initialize clockChain
			if e810Opts.PhaseInputs != nil {
				clockChain, err = InitClockChain(e810Opts, nodeProfile)
				if err != nil {
					return err
				}
				(*nodeProfile).PtpSettings["leadingInterface"] = clockChain.GetLeadingNIC().Name
				(*nodeProfile).PtpSettings["upstreamPort"] = clockChain.GetLeadingNIC().UpstreamPort
			} else {
				glog.Infof("No clock chain set: Restoring any previous pin state changes")
				err = clockChain.SetPinDefaults()
				if err != nil {
					glog.Errorf("Could not restore clockChain pin defaults: %s", err)
				}
				clockChain = &ClockChain{}
			}
		}
	}
	return nil
}

func AfterRunPTPCommandE810(data *interface{}, nodeProfile *ptpv1.PtpProfile, command string) error {
	pluginData := (*data).(*E810PluginData)
	glog.Info("calling AfterRunPTPCommandE810 for e810 plugin")
	var e810Opts E810Opts
	var err error

	e810Opts.EnableDefaultConfig = false

	for name, opts := range (*nodeProfile).Plugins {
		if name == pluginNameE810 {
			optsByteArray, _ := json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &e810Opts)
			if err != nil {
				glog.Error("e810 failed to unmarshal opts: " + err.Error())
			}
			switch command {
			case "gpspipe":
				glog.Infof("AfterRunPTPCommandE810 doing ublx config for command: %s", command)
				// Execute user-supplied UblxCmds first:
				*pluginData.hwplugins = append(*pluginData.hwplugins, e810Opts.UblxCmds.runAll()...)
				// Finish with the default commands:
				*pluginData.hwplugins = append(*pluginData.hwplugins, defaultUblxCmds().runAll()...)
			case "tbc-ho-exit":
				err = clockChain.EnterNormalTBC()
				if err != nil {
					return fmt.Errorf("e810: failed to enter T-BC normal mode")
				}
				glog.Info("e810: enter T-BC normal mode")
			case "tbc-ho-entry":
				err = clockChain.EnterHoldoverTBC()
				if err != nil {
					return fmt.Errorf("e810: failed to enter T-BC holdover")
				}
				glog.Info("e810: enter T-BC holdover")
			case "reset-to-default":
				err = clockChain.SetPinDefaults()
				if err != nil {
					return fmt.Errorf("e810: failed to reset pins to default")
				}
				glog.Info("e810: reset pins to default")
			default:
				glog.Infof("AfterRunPTPCommandE810 doing nothing for command: %s", command)
			}
		}
	}
	return nil
}

func PopulateHwConfigE810(data *interface{}, hwconfigs *[]ptpv1.HwConfig) error {
	//hwConfig := ptpv1.HwConfig{}
	//hwConfig.DeviceID = "e810"
	//*hwconfigs = append(*hwconfigs, hwConfig)
	if data != nil {
		_data := *data
		pluginData := _data.(*E810PluginData)
		_pluginData := *pluginData
		if _pluginData.hwplugins != nil {
			for _, _hwconfig := range *_pluginData.hwplugins {
				hwConfig := ptpv1.HwConfig{}
				hwConfig.DeviceID = pluginNameE810
				hwConfig.Status = _hwconfig
				*hwconfigs = append(*hwconfigs, hwConfig)
			}
		}
	}
	return nil
}

func E810(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginNameE810 {
		glog.Errorf("Plugin must be initialized as 'e810'")
		return nil, nil
	}
	glog.Infof("registering e810 plugin")
	hwplugins := []string{}
	pluginData := E810PluginData{hwplugins: &hwplugins}
	_plugin := plugin.Plugin{
		Name:               pluginNameE810,
		OnPTPConfigChange:  OnPTPConfigChangeE810,
		AfterRunPTPCommand: AfterRunPTPCommandE810,
		PopulateHwConfig:   PopulateHwConfigE810,
	}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}

func loadPins(path string) (*[]dpll_netlink.PinInfo, error) {
	pins := &[]dpll_netlink.PinInfo{}
	ptext, err := os.ReadFile(path)
	if err != nil {
		return pins, err
	}
	err = json.Unmarshal([]byte(ptext), pins)
	return pins, err
}
