package intel

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	dpll_netlink "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type E810Opts struct {
	EnableDefaultConfig bool                         `json:"enableDefaultConfig"`
	UblxCmds            UblxCmdList                  `json:"ublxCmds"`
	DevicePins          map[string]map[string]string `json:"pins"`
	DpllSettings        map[string]uint64            `json:"settings"`
	PhaseOffsetPins     map[string]map[string]string `json:"phaseOffsetPins"`
	PhaseInputs         []PhaseInputs                `json:"interconnections"`
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
	clockChain = &ClockChain{}
)

// For mocking DPLL pin info
var DpllPins = []*dpll_netlink.PinInfo{}

func OnPTPConfigChangeE810(data *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	glog.Info("calling onPTPConfigChange for e810 plugin")
	var e810Opts E810Opts
	var err error
	var optsByteArray []byte
	var stdout []byte
	var pinPath string

	e810Opts.EnableDefaultConfig = false

	for name, opts := range (*nodeProfile).Plugins {
		if name == "e810" {
			optsByteArray, _ = json.Marshal(opts)
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

			if e810Opts.EnableDefaultConfig {
				stdout, _ = exec.Command("/usr/bin/bash", "-c", EnableE810PTPConfig).Output()
				glog.Infof(string(stdout))
			}
			if (*nodeProfile).PtpSettings == nil {
				(*nodeProfile).PtpSettings = make(map[string]string)
			}
			for device, pins := range e810Opts.DevicePins {
				dpllClockIdStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				if !unitTest {
					(*nodeProfile).PtpSettings[dpllClockIdStr] = strconv.FormatUint(getClockIDE810(device), 10)
					for pin, value := range pins {
						deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", device)
						phcs, err := os.ReadDir(deviceDir)
						if err != nil {
							glog.Error("e810 failed to read " + deviceDir + ": " + err.Error())
							continue
						}
						for _, phc := range phcs {
							pinPath = fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/pins/%s", device, phc.Name(), pin)
							glog.Infof("echo %s > %s", value, pinPath)
							err = os.WriteFile(pinPath, []byte(value), 0o666)
							if err != nil {
								glog.Error("e810 failed to write " + value + " to " + pinPath + ": " + err.Error())
							}
						}
					}
				}
			}

			for k, v := range e810Opts.DpllSettings {
				if _, ok := (*nodeProfile).PtpSettings[k]; !ok {
					(*nodeProfile).PtpSettings[k] = strconv.FormatUint(v, 10)
				}
			}
			for iface, properties := range e810Opts.PhaseOffsetPins {
				ifaceFound := false
				for dev := range e810Opts.DevicePins {
					if strings.Compare(iface, dev) == 0 {
						ifaceFound = true
						break
					}
				}
				if !ifaceFound {
					glog.Errorf("e810 phase offset pin filter initialization failed: interface %s not found among  %v",
						iface, reflect.ValueOf(e810Opts.DevicePins).MapKeys())
					break
				}
				for pinProperty, value := range properties {
					key := strings.Join([]string{iface, "phaseOffsetFilter", strconv.FormatUint(getClockIDE810(iface), 10), pinProperty}, ".")
					(*nodeProfile).PtpSettings[key] = value
				}
			}
			if e810Opts.PhaseInputs != nil {
				if unitTest {
					// Mock clock chain DPLL pins in unit test
					clockChain.DpllPins = DpllPins
				}
				clockChain, err = InitClockChain(e810Opts, nodeProfile)
				if err != nil {
					return err
				}
				(*nodeProfile).PtpSettings["leadingInterface"] = clockChain.LeadingNIC.Name
				(*nodeProfile).PtpSettings["upstreamPort"] = clockChain.LeadingNIC.UpstreamPort
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
	var optsByteArray []byte

	e810Opts.EnableDefaultConfig = false

	for name, opts := range (*nodeProfile).Plugins {
		if name == "e810" {
			optsByteArray, _ = json.Marshal(opts)
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
				hwConfig.DeviceID = "e810"
				hwConfig.Status = _hwconfig
				*hwconfigs = append(*hwconfigs, hwConfig)
			}
		}
	}
	return nil
}

func E810(name string) (*plugin.Plugin, *interface{}) {
	if name != "e810" {
		glog.Errorf("Plugin must be initialized as 'e810'")
		return nil, nil
	}
	glog.Infof("registering e810 plugin")
	hwplugins := []string{}
	pluginData := E810PluginData{hwplugins: &hwplugins}
	_plugin := plugin.Plugin{
		Name:               "e810",
		OnPTPConfigChange:  OnPTPConfigChangeE810,
		AfterRunPTPCommand: AfterRunPTPCommandE810,
		PopulateHwConfig:   PopulateHwConfigE810,
	}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}

func getClockIDE810(device string) uint64 {
	const (
		PCI_EXT_CAP_ID_DSN       = 3
		PCI_CFG_SPACE_SIZE       = 256
		PCI_EXT_CAP_NEXT_OFFSET  = 2
		PCI_EXT_CAP_OFFSET_SHIFT = 4
		PCI_EXT_CAP_DATA_OFFSET  = 4
	)
	b, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/device/config", device))
	if err != nil {
		glog.Error(err)
		return 0
	}
	// Extended capability space starts right on PCI_CFG_SPACE
	var offset uint16 = PCI_CFG_SPACE_SIZE
	var id uint16
	for {
		id = binary.LittleEndian.Uint16(b[offset:])
		if id != PCI_EXT_CAP_ID_DSN {
			if id == 0 {
				glog.Errorf("can't find DSN for device %s", device)
				return 0
			}
			offset = binary.LittleEndian.Uint16(b[offset+PCI_EXT_CAP_NEXT_OFFSET:]) >> PCI_EXT_CAP_OFFSET_SHIFT
			continue
		}
		break
	}
	return binary.LittleEndian.Uint64(b[offset+PCI_EXT_CAP_DATA_OFFSET:])
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
