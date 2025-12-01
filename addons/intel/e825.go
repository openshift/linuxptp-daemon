package intel

import (
	"encoding/binary"
	"encoding/json"
	"errors"
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

var pluginNameE825 = "e825"

// E825Opts is the options structure for e825 plugin
type E825Opts struct {
	EnableDefaultConfig bool                         `json:"enableDefaultConfig"`
	UblxCmds            UblxCmdList                  `json:"ublxCmds"`
	DevicePins          map[string]map[string]string `json:"pins"`
	DeviceFreqencies    map[string][]string          `json:"frequencies"`
	DpllSettings        map[string]uint64            `json:"settings"`
	PhaseOffsetPins     map[string]map[string]string `json:"phaseOffsetPins"`
	Gnss                GnssOptions                  `json:"gnss"`
}

// GnssOptions defines GNSS-specific options for the e825
type GnssOptions struct {
	Disabled bool `json:"disabled"`
}

// E825PluginData is the data structure for e825 plugin
type E825PluginData struct {
	hwplugins *[]string
	dpllPins  []*dpll_netlink.PinInfo
}

// EnableE825PTPConfig is the script to enable default e825 PTP configuration
var EnableE825PTPConfig = `
#!/bin/bash
set -eu

echo "No E825 specific configuration is needed"
`

// OnPTPConfigChangeE825 performs actions on PTP config change for e825 plugin
func OnPTPConfigChangeE825(data *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	pluginData := (*data).(*E825PluginData)
	glog.Info("calling onPTPConfigChange for e825 plugin")
	var e825Opts E825Opts
	var err error
	var optsByteArray []byte
	for name, opts := range (*nodeProfile).Plugins {
		if name == pluginNameE825 { // "e825"
			optsByteArray, _ = json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &e825Opts)
			if err != nil {
				glog.Error("e825 failed to unmarshal opts: " + err.Error())
			}
			// for unit testing only, PtpSettings may include "unitTest" key. The value is
			// the path where resulting configuration files will be written, instead of /var/run
			_, unitTest = (*nodeProfile).PtpSettings["unitTest"]
			if unitTest {
				MockPins()
			}

			if e825Opts.EnableDefaultConfig {
				stdout, _ := exec.Command("/usr/bin/bash", "-c", EnableE825PTPConfig).Output()
				glog.Infof(string(stdout))
			}
			if (*nodeProfile).PtpSettings == nil {
				(*nodeProfile).PtpSettings = make(map[string]string)
			}

			// Prefer ZL3073x module for e825
			zlClockID, zlErr := getClockIDByModule("zl3073x")
			if zlErr != nil {
				glog.Errorf("e825: failed to resolve ZL3073x DPLL clock ID via netlink: %v", zlErr)
			}
			for device, pins := range e825Opts.DevicePins {
				dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				if !unitTest {
					if zlErr == nil {
						(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(zlClockID, 10)
					} else {
						(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(getClockIDE825(device), 10)
					}
					deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", device)
					phcs, perr := os.ReadDir(deviceDir)
					if perr != nil {
						glog.Errorf("e825 failed to read %s: %s", deviceDir, perr)
						continue
					}
					for _, phc := range phcs {
						for pin, value := range pins {
							pinPath := fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/pins/%s", device, phc.Name(), pin)
							glog.Infof("Setting \"%s\" > %s", value, pinPath)
							err = os.WriteFile(pinPath, []byte(value), 0o666)
							if err != nil {
								glog.Errorf("e825 pin write failure: %s", err)
							}
						}
						if periods, hasPeriodConfig := e825Opts.DeviceFreqencies[device]; hasPeriodConfig {
							periodPath := fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/period", device, phc.Name())
							for _, value := range periods {
								glog.Infof("Setting \"%s\" > %s", value, periodPath)
								err = os.WriteFile(periodPath, []byte(value), 0o666)
								if err != nil {
									glog.Errorf("e825 period write failure: %s", err)
								}
							}
						}
					}
				}
			}

			for k, v := range e825Opts.DpllSettings {
				if _, ok := (*nodeProfile).PtpSettings[k]; !ok {
					(*nodeProfile).PtpSettings[k] = strconv.FormatUint(v, 10)
				}
			}
			for iface, properties := range e825Opts.PhaseOffsetPins {
				ifaceFound := false
				for dev := range e825Opts.DevicePins {
					if strings.Compare(iface, dev) == 0 {
						ifaceFound = true
						break
					}
				}
				if !ifaceFound {
					glog.Errorf("e825 phase offset pin filter initialization failed: interface %s not found among  %v",
						iface, reflect.ValueOf(e825Opts.DevicePins).MapKeys())
					break
				}
				for pinProperty, value := range properties {
					var clockIDUsed uint64
					if zlErr == nil {
						clockIDUsed = zlClockID
					} else {
						clockIDUsed = getClockIDE825(iface)
					}
					key := strings.Join([]string{iface, "phaseOffsetFilter", strconv.FormatUint(clockIDUsed, 10), pinProperty}, ".")
					(*nodeProfile).PtpSettings[key] = value
				}
			}
			// Always enforce GNSS setting (default = enabled)
			pluginData.setupGnss(e825Opts.Gnss)
			// Configure leadingInterface when in BC mode
			if nodeProfile.PtpSettings["clockType"] == "T-BC" {
				if len(e825Opts.DevicePins) > 1 {
					glog.Errorf("e825 configuration expects exactly one device, but %d were configured", len(e825Opts.DevicePins))
				}
				for device := range e825Opts.DevicePins {
					glog.Infof("Configuring leading device %s pins for BC configuration", device)
					(*nodeProfile).PtpSettings["leadingInterface"] = device
					(*nodeProfile).PtpSettings["upstreamPort"] = ""
				}
			}
		}
	}
	return nil
}

func (d *E825PluginData) populateDpllPins() error {
	if unitTest {
		return nil
	}
	conn, err := dpll_netlink.Dial(nil)
	if err != nil {
		return fmt.Errorf("failed to dial DPLL: %w", err)
	}
	defer conn.Close()
	d.dpllPins, err = conn.DumpPinGet()
	if err != nil {
		return fmt.Errorf("failed to dump DPLL pins: %w", err)
	}
	return nil
}

// Setup mockable pin setting function
var e825DoPinSet = BatchPinSet

func pinCmdSetState(pin *dpll_netlink.PinInfo, connectable bool) dpll_netlink.PinParentDeviceCtl {
	newState := uint32(dpll_netlink.PinStateSelectable)
	if !connectable {
		newState = uint32(dpll_netlink.PinStateDisconnected)
	}
	command := dpll_netlink.PinParentDeviceCtl{
		ID:           pin.ID,
		PinParentCtl: make([]dpll_netlink.PinControl, 0),
	}
	for _, parent := range pin.ParentDevice {
		command.PinParentCtl = append(command.PinParentCtl, dpll_netlink.PinControl{
			PinParentID: parent.ParentID,
			State:       &newState,
		})
	}
	return command
}

func (d *E825PluginData) setupGnss(gnss GnssOptions) error {
	if len(d.dpllPins) == 0 {
		err := d.populateDpllPins()
		if err != nil {
			return fmt.Errorf("could not detect any DPLL pins: %w", err)
		}
	}
	commands := []dpll_netlink.PinParentDeviceCtl{}
	affectedPins := []string{}
	for _, pin := range d.dpllPins {
		// Look for all GNSS input pins whose state can be changed
		if pin.Type == dpll_netlink.PinTypeGNSS &&
			(pin.Capabilities&dpll_netlink.PinCapState != 0) &&
			pin.ParentDevice[0].Direction == dpll_netlink.PinDirectionInput {
			affectedPins = append(affectedPins, pin.BoardLabel)
			commands = append(commands, pinCmdSetState(pin, !gnss.Disabled))
		}
	}
	action := "enable"
	if gnss.Disabled {
		action = "disable"
	}
	if len(commands) == 0 {
		glog.Errorf("Could not locate any GNSS pins to %s", action)
		return errors.New("no GNSS pins found")
	}
	glog.Infof("Will %s %d GNSS pins: %v", action, len(commands), affectedPins)
	return e825DoPinSet(&commands)
}

// AfterRunPTPCommandE825 performs actions after certain PTP commands for e825 plugin
func AfterRunPTPCommandE825(data *interface{}, nodeProfile *ptpv1.PtpProfile, command string) error {
	pluginData := (*data).(*E825PluginData)
	glog.Info("calling AfterRunPTPCommandE825 for e825 plugin")
	var e825Opts E825Opts
	var err error
	var optsByteArray []byte

	e825Opts.EnableDefaultConfig = false

	for name, opts := range (*nodeProfile).Plugins {
		if name == "e825" {
			optsByteArray, _ = json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &e825Opts)
			if err != nil {
				glog.Error("e825 failed to unmarshal opts: " + err.Error())
			}
			switch command {
			case "gpspipe":
				glog.Infof("AfterRunPTPCommandE810 doing ublx config for command: %s", command)
				// Execute user-supplied UblxCmds first:
				*pluginData.hwplugins = append(*pluginData.hwplugins, e825Opts.UblxCmds.runAll()...)
				// Finish with the default commands:
				*pluginData.hwplugins = append(*pluginData.hwplugins, defaultUblxCmds().runAll()...)
			default:
				glog.Infof("AfterRunPTPCommandE825 doing nothing for command: %s", command)
			}
		}
	}
	return nil
}

// PopulateHwConfigE825 populates hwconfig for e825 plugin
func PopulateHwConfigE825(data *interface{}, hwconfigs *[]ptpv1.HwConfig) error {
	//hwConfig := ptpv1.HwConfig{}
	//hwConfig.DeviceID = "e825"
	//*hwconfigs = append(*hwconfigs, hwConfig)
	if data != nil {
		_data := *data
		pluginData := _data.(*E825PluginData)
		_pluginData := *pluginData
		if _pluginData.hwplugins != nil {
			for _, _hwconfig := range *_pluginData.hwplugins {
				hwConfig := ptpv1.HwConfig{}
				hwConfig.DeviceID = "e825"
				hwConfig.Status = _hwconfig
				*hwconfigs = append(*hwconfigs, hwConfig)
			}
		}
	}
	return nil
}

// E825 initializes the e825 plugin
func E825(name string) (*plugin.Plugin, *interface{}) {
	if name != "e825" {
		glog.Errorf("Plugin must be initialized as 'e825'")
		return nil, nil
	}
	glog.Infof("registering e825 plugin")
	hwplugins := []string{}
	pluginData := E825PluginData{hwplugins: &hwplugins}
	_plugin := plugin.Plugin{
		Name:               "e825",
		OnPTPConfigChange:  OnPTPConfigChangeE825,
		AfterRunPTPCommand: AfterRunPTPCommandE825,
		PopulateHwConfig:   PopulateHwConfigE825,
	}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}

func getClockIDE825(device string) uint64 {
	const (
		PCI_EXT_CAP_ID_DSN       = 3   //nolint
		PCI_CFG_SPACE_SIZE       = 256 //nolint
		PCI_EXT_CAP_NEXT_OFFSET  = 2   //nolint
		PCI_EXT_CAP_OFFSET_SHIFT = 4   //nolint
		PCI_EXT_CAP_DATA_OFFSET  = 4   //nolint
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

// getClockIDByModule returns ClockID for a given DPLL module name, preferring PPS type if present
func getClockIDByModule(module string) (uint64, error) {
	if unitTest {
		return 0, fmt.Errorf("netlink disabled in unit test")
	}
	conn, err := dpll_netlink.Dial(nil)
	if err != nil {
		return 0, err
	}
	//nolint:errcheck
	defer conn.Close()
	devices, err := conn.DumpDeviceGet()
	if err != nil {
		return 0, err
	}
	var anyID uint64
	for _, d := range devices {
		if strings.EqualFold(d.ModuleName, module) {
			if d.Type == 1 { // PPS
				return d.ClockID, nil
			}
			anyID = d.ClockID
		}
	}
	if anyID != 0 {
		return anyID, nil
	}
	return 0, fmt.Errorf("module %s DPLL not found", module)
}
