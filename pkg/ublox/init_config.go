package ublox

import (
	"fmt"
	"slices"

	"github.com/golang/glog"
)

const (
	// ProtoVersion29dot25 is 29.25
	ProtoVersion29dot25 = "29.25"
	// ProtoVersion29dot20 is 29.20
	ProtoVersion29dot20 = "29.20"

	surveyInModeCommand = "CFG-TMODE-MODE,1"
	surveyInPollType    = "TIM-SVIN"
)

// InitConfig contains the GNSS settings that should be applied before
// monitoring starts. It deliberately does not depend on the HardwareConfig
// API; hardwareconfig adapts its CRD type to this small protocol configuration.
type InitConfig struct {
	AntennaVoltage bool
	Constellations []Constellation
	SurveyIn       *SurveyInConfig
	ExtraCommands  CommandList
}

// Constellation identifies a supported GNSS constellation.
type Constellation string

const (
	// ConstellationGPS identifies the GPS constellation.
	ConstellationGPS Constellation = "GPS"
	// ConstellationQZSS identifies the QZSS constellation.
	ConstellationQZSS Constellation = "QZSS"
	// ConstellationGalileo identifies the Galileo constellation.
	ConstellationGalileo Constellation = "GAL"
	// ConstellationGLONASS identifies the GLONASS constellation (29.20 only).
	ConstellationGLONASS Constellation = "GLO"
	// ConstellationBeiDou identifies the BeiDou constellation.
	ConstellationBeiDou Constellation = "BDS"
	// ConstellationSBAS identifies the SBAS constellation.
	ConstellationSBAS Constellation = "SBAS"
	// ConstellationNAVIC identifies the NAVIC constellation.
	ConstellationNAVIC Constellation = "NAVIC"
)

// Default constellations to enable if none are specified.
var defaultConstellations = []Constellation{
	ConstellationGPS,
	ConstellationQZSS,
	ConstellationGalileo,
}

var allowedConstellations = map[string][]Constellation{
	ProtoVersion29dot20: {
		ConstellationGPS,
		ConstellationQZSS,
		ConstellationGalileo,
		ConstellationGLONASS,
		ConstellationBeiDou,
		ConstellationSBAS,
		ConstellationNAVIC,
	},
	ProtoVersion29dot25: {
		ConstellationGPS,
		ConstellationQZSS,
		ConstellationGalileo,
		ConstellationBeiDou,
		ConstellationSBAS,
		ConstellationNAVIC,
	},
}

// SurveyInConfig defines the receiver's survey-in duration and accuracy target.
type SurveyInConfig struct {
	ObservationTime int
	AccuracyMeters  int
}

// BuildInitCommands creates version-independent and version-specific GNSS
// initialization commands. Keeping this in ublox means command syntax can be
// selected after the receiver protocol version has been detected.
func BuildInitCommands(protoVersion string, config *InitConfig) CommandList {
	if config == nil {
		return nil
	}

	voltage := "0"
	if config.AntennaVoltage {
		voltage = "1"
	}
	cmds := CommandList{{Args: []string{"-z", "CFG-HW-ANT_CFG_VOLTCTRL," + voltage}}}

	cmd := config.buildConstellationCommand(protoVersion)
	if len(cmd.Args) > 0 {
		cmds = append(cmds, cmd)
	}

	if config.SurveyIn != nil && config.SurveyIn.ObservationTime > 0 {
		cmds = append(cmds,
			Command{Args: []string{
				"-z", fmt.Sprintf("CFG-TMODE-SVIN_MIN_DUR,%d", config.SurveyIn.ObservationTime),
				"-z", fmt.Sprintf("CFG-TMODE-SVIN_ACC_LIMIT,%d", config.SurveyIn.AccuracyMeters*10000),
				"-z", surveyInModeCommand,
			}},
			Command{Args: []string{"-p", surveyInPollType}, ReportOutput: true},
		)
	}
	cmds = append(cmds, config.ExtraCommands...)
	return cmds
}

func enableConstellation(constellation Constellation, enable bool) string {
	value := 0
	if enable {
		value = 1
	}
	return fmt.Sprintf("CFG-SIGNAL-%s_ENA,%d", constellation, value)
}

func (config *InitConfig) buildConstellationCommand(protoVersion string) Command {
	var all []Constellation
	all, supported := allowedConstellations[protoVersion]
	if !supported {
		glog.Warningf("Not building any constellation commands: Unsupported protocol version %s", protoVersion)
		return Command{}
	}

	// Filter the user's desired config against the version-specific set of allowed constellations
	enable := []Constellation{}
	for _, c := range all {
		if slices.Contains(config.Constellations, c) {
			enable = append(enable, c)
		}
	}
	if len(enable) == 0 {
		if len(config.Constellations) == 0 {
			glog.Warningf("No GNSS constellations are enabled, falling back to default set %v", defaultConstellations)
			enable = defaultConstellations
		} else {
			glog.Warningf("None of the requested GNSS constellations are supported by protocol version %s", protoVersion)
			return Command{}
		}
	}
	disable := []Constellation{}
	for _, c := range all {
		if !slices.Contains(enable, c) {
			disable = append(disable, c)
		}
	}

	// Wrap them into the ublox command
	constellations := Command{}
	for _, c := range enable {
		constellations.Args = append(constellations.Args, "-z", enableConstellation(c, true))
	}
	for _, c := range disable {
		constellations.Args = append(constellations.Args, "-z", enableConstellation(c, false))
	}
	return constellations
}
