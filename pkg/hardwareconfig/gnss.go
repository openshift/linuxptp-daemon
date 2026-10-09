package hardwareconfig

import (
	"fmt"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

// GetGNSSSerialPort finds the GNSS source in the hardware config for the given
// profile and resolves its TTY device path. Returns empty string if no GNSS
// source is configured or the profile has no hardware config.
func (hcm *HardwareConfigManager) GetGNSSSerialPort(nodeProfile *ptpv1.PtpProfile) (string, error) {
	source, _ := hcm.findGNSSSource(nodeProfile)
	if source == nil {
		return "", nil
	}
	return FindGNSSDevice(source.GNSSConfig.Match)
}

// GetGNSSInitConfig returns the GNSS settings for the configured source. The
// ublox package builds the actual commands after detecting the receiver version.
func (hcm *HardwareConfigManager) GetGNSSInitConfig(nodeProfile *ptpv1.PtpProfile) *ublox.InitConfig {
	_, config := hcm.findGNSSSource(nodeProfile)
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
// given profile. Returns the source config and its GNSSConfig, or nil if none found.
func (hcm *HardwareConfigManager) findGNSSSource(nodeProfile *ptpv1.PtpProfile) (*ptpv2alpha1.SourceConfig, *ptpv2alpha1.GNSSConfig) {
	for _, profile := range hcm.GetHardwareConfigsForProfile(nodeProfile) {
		if profile.ClockChain == nil || profile.ClockChain.Behavior == nil {
			continue
		}
		for i, source := range profile.ClockChain.Behavior.Sources {
			if source.SourceType == ptpv2alpha1.SourceTypeGNSS && source.GNSSConfig != nil {
				return &profile.ClockChain.Behavior.Sources[i], source.GNSSConfig
			}
		}
	}
	return nil, nil
}

// FindGNSSDevice resolves the GNSS TTY device path from a GNSSMatcher.
// If matcher specifies a TTYDevice, it is returned directly.
// If matcher specifies an EthernetInterface, the device is looked up via sysfs.
// If matcher is nil, returns empty string (caller should auto-detect).
func FindGNSSDevice(matcher *ptpv2alpha1.GNSSMatcher) (string, error) {
	if matcher == nil {
		return "", nil
	}
	if matcher.TTYDevice != "" {
		return matcher.TTYDevice, nil
	}
	if matcher.EthernetInterface != "" {
		return ublox.GNSSDeviceFromInterface(matcher.EthernetInterface)
	}
	return "", fmt.Errorf("GNSSMatcher has neither ttyDevice nor ethernetInterface set")
}
