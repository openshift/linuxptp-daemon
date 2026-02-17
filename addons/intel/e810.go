package intel

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

var pluginNameE810 = "e810"

type E810Opts struct {
	PluginOpts
	EnableDefaultConfig bool          `json:"enableDefaultConfig"`
	UblxCmds            UblxCmdList   `json:"ublxCmds"`
	PhaseInputs         []PhaseInputs `json:"interconnections"`
	Gnss                GnssOptions   `json:"gnss"`
}

// GetPhaseInputs implements PhaseInputsProvider
func (o E810Opts) GetPhaseInputs() []PhaseInputs { return o.PhaseInputs }

type E810PluginData struct {
	PluginData
}

var (
	clockChain ClockChainInterface = &ClockChain{DpllPins: DpllPins}

	// defaultE810PinConfig -> All outputs disabled
	defaultE810PinConfig = pinSet{
		"SMA1":  "0 1",
		"SMA2":  "0 2",
		"U.FL1": "0 1",
		"U.FL2": "0 2",
	}
)

func OnPTPConfigChangeE810(data *interface{}, nodeProfile *ptpv1.PtpProfile) error {
	glog.Info("calling onPTPConfigChange for e810 plugin")

	autoDetectGNSSSerialPort(nodeProfile)
	checkPinIndex(nodeProfile)

	var e810Opts E810Opts
	e810Opts.Gnss.LeapSources = defaultLeapSourceOptions()
	var err error

	e810Opts.EnableDefaultConfig = false

	err = DpllPins.FetchPins()
	if err != nil {
		return err
	}

	for name, opts := range (*nodeProfile).Plugins {
		if name == pluginNameE810 {
			optsByteArray, _ := json.Marshal(opts)

			// Validate configuration before applying
			if validationErrors := ValidateE810Opts(optsByteArray); len(validationErrors) > 0 {
				return fmt.Errorf("e810 plugin configuration errors: %s", strings.Join(validationErrors, "; "))
			}

			err = json.Unmarshal(optsByteArray, &e810Opts)
			if err != nil {
				return fmt.Errorf("e810 failed to unmarshal opts: %w", err)
			}

			allDevices := e810Opts.allDevices()

			clockIDs := make(map[string]uint64)

			if (*nodeProfile).PtpSettings == nil {
				(*nodeProfile).PtpSettings = make(map[string]string)
			}

			glog.Infof("Initializing e810 plugin for profile %s and devices %v", *nodeProfile.Name, allDevices)
			for _, device := range allDevices {
				dpllClockIDStr := fmt.Sprintf("%s[%s]", dpll.ClockIdStr, device)
				clkID := getClockID(device)
				if clkID == 0 {
					glog.Errorf("failed to get clockID for device %s; pins for this device will not be configured", device)
				}
				clockIDs[device] = clkID
				(*nodeProfile).PtpSettings[dpllClockIDStr] = strconv.FormatUint(clkID, 10)
			}

			for device, frequencies := range e810Opts.DeviceFreqencies {
				err = pinConfig.applyPinFrq(device, frequencies)
				if err != nil {
					return fmt.Errorf("e810 failed to set PHC frequencies for %s: %w", device, err)
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
					key := strings.Join([]string{iface, "phaseOffsetFilter", strconv.FormatUint(getClockID(iface), 10), pinProperty}, ".")
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
					return fmt.Errorf("could not restore clockChain pin defaults: %s", err)
				}
				clockChain = &ClockChain{DpllPins: DpllPins}
				err = DpllPins.FetchPins()
				if err != nil {
					glog.Errorf("Could not determine the current state of the dpll pins: %s", err)
				}
			}

			if e810Opts.EnableDefaultConfig {
				for _, device := range allDevices {
					if hasSysfsSMAPins(device) {
						err = pinConfig.applyPinSet(device, defaultE810PinConfig)
					} else {
						err = DpllPins.ApplyPinCommands(DpllPins.GetCommandsForPluginPinSet(clockIDs[device], defaultE810PinConfig))
					}
					if err != nil {
						glog.Errorf("e810 failed to set default Pin configuration for %s: %s", device, err)
					}
				}
			}

			// Initialize all user-specified phc pins and frequencies
			for device, pins := range e810Opts.DevicePins {
				if hasSysfsSMAPins(device) {
					err = pinConfig.applyPinSet(device, pins)
				} else {
					commands := DpllPins.GetCommandsForPluginPinSet(clockIDs[device], pins)
					if pinSetHasSMAInput(pins) {
						gnssPin := DpllPins.GetByLabel(gnss, clockIDs[device])
						if gnssPin != nil {
							gnssCommands := SetPinControlData(*gnssPin, PinParentControl{
								EecPriority: 4,
								PpsPriority: 4,
							})
							commands = append(commands, gnssCommands...)
						} else {
							glog.Warningf("SMA input detected but GNSS-1PPS pin not found for clockID %d", clockIDs[device])
						}
					}
					err = DpllPins.ApplyPinCommands(commands)
				}
				if err != nil {
					glog.Errorf("e810 failed to set Pin configuration for %s: %s", device, err)
				}
			}

			updateLeapManagerSources(e810Opts.Gnss.LeapSources)
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
				return fmt.Errorf("e810 failed to unmarshal opts: %w", err)
			}
			switch command {
			case "gpspipe":
				glog.Infof("AfterRunPTPCommandE810 doing ublx config for command: %s", command)
				// Execute user-supplied UblxCmds first:
				pluginData.hwplugins = append(pluginData.hwplugins, e810Opts.UblxCmds.runAll(true)...)
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

func E810(name string) (*plugin.Plugin, *interface{}) {
	if name != pluginNameE810 {
		glog.Errorf("Plugin must be initialized as 'e810'")
		return nil, nil
	}
	glog.Infof("registering e810 plugin")
	pluginData := E810PluginData{
		PluginData: PluginData{name: pluginNameE810},
	}
	_plugin := plugin.Plugin{
		Name:               pluginNameE810,
		OnPTPConfigChange:  OnPTPConfigChangeE810,
		AfterRunPTPCommand: AfterRunPTPCommandE810,
		PopulateHwConfig:   pluginData.PopulateHwConfig,
	}
	var iface interface{} = &pluginData
	return &_plugin, &iface
}

func pinSetHasSMAInput(pins pinSet) bool {
	for label, value := range pins {
		if (label == "SMA1" || label == "SMA2") &&
			strings.HasPrefix(strings.TrimSpace(value), "1") {
			return true
		}
	}
	return false
}

func checkPinIndex(nodeProfile *ptpv1.PtpProfile) {
	if nodeProfile.Ts2PhcConf == nil {
		return
	}

	profileName := ""
	if nodeProfile.Name != nil {
		profileName = *nodeProfile.Name
	}

	lines := strings.Split(*nodeProfile.Ts2PhcConf, "\n")
	result := make([]string, 0, len(lines)+1)
	shouldAddPinIndex := false
	for _, line := range lines {
		trimedLine := strings.TrimSpace(line)
		if strings.HasPrefix(trimedLine, "[") && strings.HasSuffix(trimedLine, "]") {
			// We went through the previous entry and didn't find a pin index
			if shouldAddPinIndex {
				glog.Infof("Adding 'ts2phc.pin_index 1' to ts2phc for profile name %s", profileName)
				result = append(result, "ts2phc.pin_index 1")
				shouldAddPinIndex = false
			}

			ifName := strings.TrimSpace(strings.TrimRight(strings.TrimLeft(trimedLine, "["), "]"))
			if ifName != "global" && ifName != "nmea" && !hasSysfsSMAPins(ifName) {
				shouldAddPinIndex = true
			}
		}
		if strings.HasPrefix(trimedLine, "ts2phc.pin_index") || strings.HasPrefix(trimedLine, "ts2phc.pin_name") {
			shouldAddPinIndex = false
		}
		result = append(result, line)
	}
	if shouldAddPinIndex {
		glog.Infof("Adding 'ts2phc.pin_index 1' to ts2phc for profile name %s", profileName)
		result = append(result, "ts2phc.pin_index 1")
	}

	updatedTs2phcConfig := strings.Join(result, "\n")
	nodeProfile.Ts2PhcConf = &updatedTs2phcConfig
}
