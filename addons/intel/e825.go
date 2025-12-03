package intel

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	dpll_netlink "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

var pluginNameE825 = "e825"

// Hard-coded pin configuration to enforce when T-BC is initializing or loses sync
var bcDpllPinReset = pinSet{
	// Disable SDP0
	"SDP0": "0 0",
	// Disable SDP2
	"SDP2": "0 0",
}

// Hard-coded pin configuration to setup PHY-to-DPLL sync when T-BC upstreamPort is synchronized
var bcDpllPinSetup = pinSet{
	// SDP0 TX on channel 1
	"SDP0": "2 1",
	// SDP2 TX on channel 2
	"SDP2": "2 2",
}

// Hard-coded pin frequencies to setup PHY-to-DPLL sync when T-BC upstreamPort is synchronized
var bcDpllPeriods = frqSet{
	// channel 1 (SDP0) configure 1PPS
	"1 0 0 1 0",
	// channel 2 (SDP2) configure 1kHz
	"2 0 0 0 1000000",
}

// E825Opts is the options structure for e825 plugin
type E825Opts struct {
	UblxCmds         UblxCmdList                  `json:"ublxCmds"`
	Devices          []string                     `json:"devices"`
	DevicePins       map[string]pinSet            `json:"pins"`
	DeviceFreqencies map[string]frqSet            `json:"frequencies"`
	DpllSettings     map[string]uint64            `json:"settings"`
	PhaseOffsetPins  map[string]map[string]string `json:"phaseOffsetPins"`
	Gnss             GnssOptions                  `json:"gnss"`
}

// GnssOptions defines GNSS-specific options for the e825
type GnssOptions struct {
	Disabled bool `json:"disabled"`
}

// allDevices enumerates all defined devices (Devices/DevicePins/DeviceFrequencies/PhaseOffsets)
func (opts *E825Opts) allDevices() []string {
	// Enumerate all defined devices (Devices/DevicePins/DeviceFrequencies)
	allDevices := opts.Devices
	allDevices = extendWithKeys(allDevices, opts.DevicePins)
	allDevices = extendWithKeys(allDevices, opts.DeviceFreqencies)
	allDevices = extendWithKeys(allDevices, opts.PhaseOffsetPins)
	return allDevices
}

// E825PluginData is the data structure for e825 plugin
type E825PluginData struct {
	hwplugins *[]string
	dpllPins  []*dpll_netlink.PinInfo
}

func tbcConfigured(nodeProfile *ptpv1.PtpProfile) bool {
	return nodeProfile.PtpSettings["clockType"] == "T-BC"
}

func extendWithKeys[T any](s []string, m map[string]T) []string {
	for key := range m {
		if !slices.Contains(s, key) {
			s = append(s, key)
		}
	}
	return s
}

// OnPTPConfigChangeE825 performs actions on PTP config change for e825 plugin
func OnPTPConfigChangeE825(data *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	pluginData := (*data).(*E825PluginData)
	glog.Infof("calling onPTPConfigChange for e825 plugin (%s)", *nodeProfile.Name)
	var e825Opts E825Opts
	var err error
	var optsByteArray []byte

	if (*nodeProfile).PtpSettings == nil {
		(*nodeProfile).PtpSettings = make(map[string]string)
	}

	if tbcConfigured(nodeProfile) {
		// For T-BC, default to GNSS=disabled, but allow manual override in the user-speciifed config
		e825Opts.Gnss.Disabled = true
	}

	for name, opts := range (*nodeProfile).Plugins {
		if name == pluginNameE825 {
			// Parse user-specified config
			optsByteArray, _ = json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &e825Opts)
			if err != nil {
				glog.Error("e825 failed to unmarshal opts: " + err.Error())
			}

			allDevices := e825Opts.allDevices()
			glog.Infof("Initializing e825 plugin for profile %s and devices %v", *nodeProfile.Name, allDevices)

			// Setup clockID (prefer ZL3073x module clock ID for e825)
			zlClockID, zlErr := getClockIDByModule("zl3073x")
			if zlErr != nil {
				glog.Errorf("e825: failed to resolve ZL3073x DPLL clock ID via netlink: %v", zlErr)
			}
			for _, device := range allDevices {
				dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				clkID := zlClockID
				if zlErr != nil {
					clkID = getClockIDE825(device)
				}
				(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(clkID, 10)
				glog.Infof("Detected %s=%d (%x)", dpllClockIDStr, clkID, clkID)
			}

			// Initialize all user-specified phc pins and frequencies
			for device, pins := range e825Opts.DevicePins {
				err = pinConfig.applyPinSet(device, pins)
				if err != nil {
					glog.Errorf("e825 failed to set Pin configuration for %s: %s", device, err)
				}
			}
			for device, frequencies := range e825Opts.DeviceFreqencies {
				err = pinConfig.applyPinFrq(device, frequencies)
				if err != nil {
					glog.Errorf("e825 failed to set PHC frequencies for %s: %s", device, err)
				}
			}

			// Copy DPLL Settings from plugin config to PtpSettings
			for k, v := range e825Opts.DpllSettings {
				if _, ok := (*nodeProfile).PtpSettings[k]; !ok {
					(*nodeProfile).PtpSettings[k] = strconv.FormatUint(v, 10)
				}
			}

			// Copy PhaseOffsetPins settings from plugin config to PtpSettings
			for iface, properties := range e825Opts.PhaseOffsetPins {
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

			// BC sanity check and pin setup
			if tbcConfigured(nodeProfile) {
				if _, ok := nodeProfile.PtpSettings["upstreamPort"]; !ok {
					return errors.New("GNR-D T-BC must set upstreamPort")
				}
				if _, ok := nodeProfile.PtpSettings["leadingInterface"]; !ok {
					// TODO: We could actually figure this out based on upstreamPort... And the fact that there's only one NAC per GNR-D
					return errors.New("GNR-D T-BC must set leadingInterface")
				}
				device := nodeProfile.PtpSettings["leadingInterface"]
				err = pinConfig.applyPinSet(device, bcDpllPinReset)
				if err != nil {
					glog.Errorf("Could not apply BC pin reset to %s: %s", device, err)
				}
			}
		}
	}
	return nil
}

// populateDpllPins creates a list of all known DPLL pins
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

// pinCmdSetState sets the state of an individual DPLL pin
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

// setupGnss configures the GNSS-to-DPLL binding
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
	glog.Infof("calling AfterRunPTPCommandE825 for e825 plugin (%s): %s", *nodeProfile.Name, command)
	var e825Opts E825Opts
	var err error
	var optsByteArray []byte

	for name, opts := range (*nodeProfile).Plugins {
		if name == pluginNameE825 {
			optsByteArray, _ = json.Marshal(opts)
			err = json.Unmarshal(optsByteArray, &e825Opts)
			if err != nil {
				glog.Error("e825 failed to unmarshal opts: " + err.Error())
			}
			switch command {
			// "gpspipe" is called once the gpspipe process is running (and we can send ublx commands)
			case "gpspipe":
				glog.Infof("AfterRunPTPCommandE825 doing ublx config for command: %s", command)
				// Execute user-supplied UblxCmds first:
				*pluginData.hwplugins = append(*pluginData.hwplugins, e825Opts.UblxCmds.runAll()...)
				// Finish with the default commands:
				*pluginData.hwplugins = append(*pluginData.hwplugins, defaultUblxCmds().runAll()...)
			// "tbc-ho-exit" is called when ptp4l sync is achieved on the T-BC upstreamPort
			case "tbc-ho-exit":
				if tbcConfigured(nodeProfile) {
					if device, devOk := nodeProfile.PtpSettings["leadingInterface"]; devOk {
						glog.Infof("Configuring NAC-to-DPLL pins for %s", device)
						err = pinConfig.applyPinSet(device, bcDpllPinSetup)
						if err != nil {
							glog.Errorf("Could not apply BC pin reset to %s: %s", device, err)
						}
						err = pinConfig.applyPinFrq(device, bcDpllPeriods)
						if err != nil {
							glog.Errorf("Could not apply BC pin periods to %s: %s", device, err)
						}
					}
				}
			// "tbc-ho-entry" is called when ptp4l sync is lost on the T-BC upstreamPort
			case "tbc-ho-entry":
				if tbcConfigured(nodeProfile) {
					if device, devOk := nodeProfile.PtpSettings["leadingInterface"]; devOk {
						glog.Infof("Resetting DPLL pins for %s", device)
						err = pinConfig.applyPinSet(device, bcDpllPinReset)
						if err != nil {
							glog.Errorf("Could not apply BC pin reset to %s: %s", device, err)
						}
					}
				}
			default:
				glog.Infof("AfterRunPTPCommandE825 doing nothing for command: %s", command)
			}
		}
	}
	return nil
}

// PopulateHwConfigE825 populates hwconfig for e825 plugin
func PopulateHwConfigE825(data *interface{}, hwconfigs *[]ptpv1.HwConfig) error {
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
