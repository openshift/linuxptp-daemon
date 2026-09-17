package hardwareconfig

import (
	"fmt"
	"slices"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

const (
	surveyInWait = "5"
)

// ubxtoolConstellationName maps ConstellationID to the name used by ubxtool -e / -d.
// Most names match directly; GLONAS is the API spelling while ubxtool uses GLONASS.
var ubxtoolConstellationName = map[ptpv2alpha1.ConstellationID]string{
	ptpv2alpha1.ConstellationGPS:     "GPS",
	ptpv2alpha1.ConstellationGalileo: "GALILEO",
	ptpv2alpha1.ConstellationGLONASS: "GLONASS",
	ptpv2alpha1.ConstellationBeiDou:  "BEIDOU",
	ptpv2alpha1.ConstellationSBAS:    "SBAS",
}

// allConstellationIDs lists all known ConstellationIDs in a stable order for
// deterministic enable/disable command generation.
var allConstellationIDs = []ptpv2alpha1.ConstellationID{
	ptpv2alpha1.ConstellationGPS,
	ptpv2alpha1.ConstellationGalileo,
	ptpv2alpha1.ConstellationGLONASS,
	ptpv2alpha1.ConstellationBeiDou,
	ptpv2alpha1.ConstellationSBAS,
}

// antennaVoltageCommand returns the command to enable or disable antenna voltage
// (CFG-HW-ANT_CFG_VOLTCTRL).
func antennaVoltageCommand(enabled bool) ublox.Command {
	val := "0"
	if enabled {
		val = "1"
	}
	return ublox.Command{
		Args: []string{"-z", fmt.Sprintf("CFG-HW-ANT_CFG_VOLTCTRL,%s", val)},
	}
}

// constellationCommand returns a single batched command that enables the
// requested constellations and disables any known constellations not in the list.
// Uses ubxtool's -e (enable) and -d (disable) flags with constellation names.
func constellationCommand(enabled []ptpv2alpha1.ConstellationID) ublox.Command {
	cmd := ublox.Command{}
	for _, id := range allConstellationIDs {
		name := ubxtoolConstellationName[id]
		if slices.Contains(enabled, id) {
			cmd.Args = append(cmd.Args, "-e", name)
		} else {
			cmd.Args = append(cmd.Args, "-d", name)
		}
	}
	return cmd
}

// surveyInCommand returns the command to start a GNSS survey-in operation.
// Uses ubxtool's -e SURVEYIN,<duration_s>,<accuracy_0.1mm> syntax with the
// appropriate wait time and verbosity for survey acknowledgment.
func surveyInCommand(survey ptpv2alpha1.GNSSSurveyParameters) ublox.Command {
	// GNSSSurveyParameters.Accuracy is in meters; ubxtool wants 0.1mm units
	accLimit := survey.Accuracy * 10000

	return ublox.Command{
		Args: []string{
			"-t", "-w", surveyInWait, "-v", "1",
			"-e", fmt.Sprintf("SURVEYIN,%d,%d", survey.ObservationTime, accLimit),
		},
		ReportOutput: true,
	}
}

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
			result.Constellations = append(result.Constellations, ublox.ConstellationGPS)
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

// buildGNSSInitCommands converts a GNSSConfig into the ordered list of ubxtool commands.
func buildGNSSInitCommands(config *ptpv2alpha1.GNSSConfig) ublox.CommandList {
	var cmds ublox.CommandList

	// 1. Antenna voltage control
	cmds = append(cmds, antennaVoltageCommand(config.Init.AntennaVoltage))

	// 2. Constellation enable/disable
	cmds = append(cmds, constellationCommand(config.Init.Constellations))

	// 3. Survey-in parameters (skip if observation time is zero)
	if config.Init.SurveyIn.ObservationTime > 0 {
		cmds = append(cmds, surveyInCommand(config.Init.SurveyIn))
	}

	// 4. User-supplied extra commands
	for _, extra := range config.Init.ExtraCommands {
		cmds = append(cmds, ublox.Command{
			Args:         extra.Args,
			ReportOutput: extra.Record,
		})
	}

	return cmds
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
