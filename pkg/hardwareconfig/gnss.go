package hardwareconfig

import (
	"fmt"
	"slices"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

// GetGNSSSerialPort finds the GNSS source in the hardware config for the given
// profile and resolves its TTY device path. It returns an empty string and nil
// error if no GNSS source is configured or the profile has no hardware config,
// and returns an error if a configured source's serial port cannot be discovered.
func (hcm *HardwareConfigManager) GetGNSSSerialPort(nodeProfile *ptpv1.PtpProfile) (string, error) {
	config, hwConfigName, sourceName := hcm.findGNSSSource(nodeProfile)
	if sourceName == "" {
		// No GNSS source found; return silently.
		return "", nil
	}
	var ttyDevice string
	var matcher *ptpv2alpha1.GNSSMatcher
	var err error
	if config == nil {
		err = fmt.Errorf("no GNSS configuration defined")
	} else {
		matcher = config.Match
		ttyDevice, err = findGNSSDevice(matcher)
	}
	hcm.recordGNSSMatchResult(hwConfigName, sourceName, ttyDevice, matcher, err)
	return ttyDevice, err
}

// HasExplicitGNSSMatcher reports whether the selected GNSS source has a matcher
// configured. Callers can use this to distinguish an intentional device
// selection that failed from legacy configurations that rely on fallback.
func (hcm *HardwareConfigManager) HasExplicitGNSSMatcher(nodeProfile *ptpv1.PtpProfile) bool {
	config, _, sourceName := hcm.findGNSSSource(nodeProfile)
	return sourceName != "" && config != nil && config.Match != nil
}

func (hcm *HardwareConfigManager) recordGNSSMatchResult(configName, sourceName, ttyDevice string, matcher *ptpv2alpha1.GNSSMatcher, matchErr error) {
	status := &ptpv2alpha1.GNSSStatus{TTYDevice: ttyDevice}
	if matchErr != nil {
		status.TTYDevice = ""
		status.MatchResult = fmt.Sprintf("GNSS device matching failed: %v", matchErr)
	} else {
		status.MatchResult = describeGNSSMatch(matcher)
	}
	glog.Infof("Computed GNSS match status for HardwareConfig %q source %q: ttyDevice=%q matchResult=%q", configName, sourceName, status.TTYDevice, status.MatchResult)

	callback := hcm.updateGnssStatus(configName, sourceName, status)
	if callback != nil {
		callback()
	}
}

func describeGNSSMatch(matcher *ptpv2alpha1.GNSSMatcher) string {
	switch {
	case matcher == nil:
		return "Matched GNSS tty without a configured matcher"
	case matcher.TTYDevice != "":
		return fmt.Sprintf("Matched GNSS tty specified directly as %s", matcher.TTYDevice)
	case matcher.SerialDevice != nil && matcher.SerialDevice.ACPI != nil:
		acpi := matcher.SerialDevice.ACPI
		if acpi.UID != "" {
			return fmt.Sprintf("Matched GNSS tty exposed by ACPI serial device %s:%s", acpi.HID, acpi.UID)
		}
		return fmt.Sprintf("Matched GNSS tty exposed by ACPI serial device %s", acpi.HID)
	case matcher.EthernetDevice != nil:
		selectors := make([]string, 0, 6)
		device := matcher.EthernetDevice
		for _, selector := range []struct{ name, value string }{
			{"name", device.Name},
			{"pciAddress", device.PCIAddress},
			{"permanentMACAddress", device.PermanentMACAddress},
			{"slot", device.Slot},
			{"vendorID", device.VendorID},
			{"deviceID", device.DeviceID},
		} {
			if selector.value != "" {
				selectors = append(selectors, fmt.Sprintf("%s=%s", selector.name, selector.value))
			}
		}
		return fmt.Sprintf("Matched GNSS tty exposed by Ethernet device (%s)", strings.Join(selectors, ", "))
	case matcher.USBDevice != nil:
		usb := matcher.USBDevice
		result := fmt.Sprintf("Matched GNSS tty exposed by USB device %s:%s", usb.Vendor, usb.Product)
		if usb.Path != "" {
			result += fmt.Sprintf(" at path %s", usb.Path)
		}
		return result
	default:
		return "Matched GNSS tty"
	}
}

func (hcm *HardwareConfigManager) updateGnssStatus(configName, sourceName string, status *ptpv2alpha1.GNSSStatus) func() {
	hcm.mu.Lock()
	defer hcm.mu.Unlock()

	configIndex := slices.IndexFunc(hcm.hardwareConfigs, func(cfg enrichedHardwareConfig) bool {
		return cfg.Name == configName
	})
	if configIndex < 0 {
		glog.Warningf("Cannot record GNSS match status: HardwareConfig %q is not in the manager", configName)
		return nil
	}

	sourceStatuses := hcm.hardwareConfigs[configIndex].Status.Sources
	statusIndex := slices.IndexFunc(sourceStatuses, func(status ptpv2alpha1.SourceStatus) bool {
		return status.Name == sourceName
	})

	if statusIndex < 0 {
		glog.Infof("Recording initial GNSS status for HardwareConfig %q source %q: %+v", configName, sourceName, status)
		// No matching status; create a new one:
		hcm.hardwareConfigs[configIndex].Status.Sources = append(sourceStatuses, ptpv2alpha1.SourceStatus{
			Name: sourceName,
			Gnss: status,
		})
		// Return cache-update callback function
		return hcm.gnssStatusChanged
	}

	previous := sourceStatuses[statusIndex].Gnss
	if previous == nil || *previous != *status {
		glog.Infof("Updating GNSS status for HardwareConfig %q source %q: previous=%+v new=%+v", configName, sourceName, previous, status)
		// No GNSS status, or the GNSS status is different; Update:
		hcm.hardwareConfigs[configIndex].Status.Sources[statusIndex].Gnss = status
		// Return cache-update callback function
		return hcm.gnssStatusChanged
	}
	glog.V(2).Infof("GNSS status for HardwareConfig %q source %q is unchanged: %+v", configName, sourceName, status)
	return nil
}

// GetGNSSInitConfig returns the GNSS settings for the configured source. The
// ublox package builds the actual commands after detecting the receiver version.
func (hcm *HardwareConfigManager) GetGNSSInitConfig(nodeProfile *ptpv1.PtpProfile) *ublox.InitConfig {
	config, _, _ := hcm.findGNSSSource(nodeProfile)
	if config == nil {
		return nil
	}
	result := &ublox.InitConfig{AntennaVoltage: config.Init.AntennaVoltage}
	for _, constellation := range config.Init.Constellations {
		switch constellation {
		case ptpv2alpha1.ConstellationGPS:
			// Weird UBLOX legacy quirk -> "GPS" enables both the 'GPS' and 'QZSS' constellations
			result.Constellations = append(result.Constellations, ublox.ConstellationGPS, ublox.ConstellationQZSS)
		case ptpv2alpha1.ConstellationGalileo:
			result.Constellations = append(result.Constellations, ublox.ConstellationGalileo)
		case ptpv2alpha1.ConstellationGLONASS:
			result.Constellations = append(result.Constellations, ublox.ConstellationGLONASS)
		case ptpv2alpha1.ConstellationBeiDou:
			result.Constellations = append(result.Constellations, ublox.ConstellationBeiDou)
		case ptpv2alpha1.ConstellationSBAS:
			result.Constellations = append(result.Constellations, ublox.ConstellationSBAS)
		}
	}
	if config.Init.SurveyIn.ObservationTime > 0 {
		result.SurveyIn = &ublox.SurveyInConfig{
			ObservationTime: config.Init.SurveyIn.ObservationTime,
			AccuracyMeters:  config.Init.SurveyIn.Accuracy,
		}
	}
	for _, extra := range config.Init.ExtraCommands {
		result.ExtraCommands = append(result.ExtraCommands, ublox.Command{Args: extra.Args, ReportOutput: extra.Record})
	}
	return result
}

// findGNSSSource locates the first GNSS source in the hardware configs for the
// given profile. It returns a deep copy of the GNSSCConfig, plus the name of
// the HardwareConfig and Source that it selected.
func (hcm *HardwareConfigManager) findGNSSSource(nodeProfile *ptpv1.PtpProfile) (config *ptpv2alpha1.GNSSConfig, hwconfigName string, sourceName string) {
	if nodeProfile == nil || nodeProfile.Name == nil {
		return nil, "", ""
	}

	hcm.mu.RLock()
	defer hcm.mu.RUnlock()
	for i := range hcm.hardwareConfigs {
		hwConfig := &hcm.hardwareConfigs[i].HardwareConfig
		if !ProfileNamesMatch(*nodeProfile.Name, hwConfig.Spec.RelatedPtpProfileName) ||
			hwConfig.Spec.Profile.ClockChain == nil || hwConfig.Spec.Profile.ClockChain.Behavior == nil {
			continue
		}
		for j := range hwConfig.Spec.Profile.ClockChain.Behavior.Sources {
			source := &hwConfig.Spec.Profile.ClockChain.Behavior.Sources[j]
			if source.SourceType == ptpv2alpha1.SourceTypeGNSS {
				config = nil
				if source.GNSSConfig != nil {
					config = source.GNSSConfig.DeepCopy()
				}
				return config, hwConfig.Name, source.Name
			}
		}
	}
	return nil, "", ""
}

// findGNSSDevice resolves the GNSS TTY device path from a GNSSMatcher.
// If matcher specifies a TTYDevice, it is returned directly.
// If matcher specifies a SerialDevice, the tty is looked up by its stable
// platform hardware identity through sysfs.
// If matcher specifies an EthernetDevice, the device is looked up via sysfs.
// If matcher specifies a USBDevice, the tty exposed by the matching USB device
// is looked up via sysfs.
// If matcher is nil, returns an error
func findGNSSDevice(matcher *ptpv2alpha1.GNSSMatcher) (string, error) {
	if matcher == nil {
		return "", fmt.Errorf("no GNSSMatcher is defined for this GNSS source")
	}
	if matcher.TTYDevice != "" {
		return matcher.TTYDevice, nil
	}
	if matcher.SerialDevice != nil && matcher.SerialDevice.ACPI != nil {
		return ublox.GNSSDeviceFromACPIDevice(
			matcher.SerialDevice.ACPI.HID,
			matcher.SerialDevice.ACPI.UID,
		)
	}
	if matcher.EthernetDevice != nil {
		return ublox.GNSSDeviceFromEthernetDevice(
			matcher.EthernetDevice.Name,
			matcher.EthernetDevice.PCIAddress,
			matcher.EthernetDevice.PermanentMACAddress,
			matcher.EthernetDevice.Slot,
			matcher.EthernetDevice.VendorID,
			matcher.EthernetDevice.DeviceID,
		)
	}
	if matcher.USBDevice != nil {
		return ublox.GNSSDeviceFromUSB(matcher.USBDevice.Vendor, matcher.USBDevice.Product, matcher.USBDevice.Path)
	}
	return "", fmt.Errorf("GNSSMatcher has neither ttyDevice, serialDevice, ethernetDevice, nor usbDevice set")
}
