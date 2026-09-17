package ublox

import "fmt"

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
	// ConstellationGalileo identifies the Galileo constellation.
	ConstellationGalileo Constellation = "GALILEO"
	// ConstellationGLONASS identifies the GLONASS constellation.
	ConstellationGLONASS Constellation = "GLONASS"
	// ConstellationBeiDou identifies the BeiDou constellation.
	ConstellationBeiDou Constellation = "BEIDOU"
	// ConstellationSBAS identifies the SBAS constellation.
	ConstellationSBAS Constellation = "SBAS"
)

// SurveyInConfig defines the receiver's survey-in duration and accuracy target.
type SurveyInConfig struct {
	ObservationTime int
	AccuracyMeters  int
}

// BuildInitCommands creates version-independent and version-specific GNSS
// initialization commands. Keeping this in ublox means command syntax can be
// selected after the receiver protocol version has been detected.
func BuildInitCommands(_ string, config *InitConfig) CommandList {
	if config == nil {
		return nil
	}

	voltage := "0"
	if config.AntennaVoltage {
		voltage = "1"
	}
	cmds := CommandList{{Args: []string{"-z", "CFG-HW-ANT_CFG_VOLTCTRL," + voltage}}}

	all := []Constellation{ConstellationGPS, ConstellationGalileo, ConstellationGLONASS, ConstellationBeiDou, ConstellationSBAS}
	constellations := Command{}
	for _, c := range all {
		name := string(c)
		// Protocol-specific command differences belong here. The current syntax
		// is valid for the supported protocol versions.
		if containsConstellation(config.Constellations, c) {
			constellations.Args = append(constellations.Args, "-e", name)
		} else {
			constellations.Args = append(constellations.Args, "-d", name)
		}
	}
	cmds = append(cmds, constellations)

	if config.SurveyIn != nil && config.SurveyIn.ObservationTime > 0 {
		cmds = append(cmds, Command{Args: []string{
			"-t", "-w", "5", "-v", "1", "-e",
			fmt.Sprintf("SURVEYIN,%d,%d", config.SurveyIn.ObservationTime, config.SurveyIn.AccuracyMeters*10000),
		}, ReportOutput: true})
	}
	cmds = append(cmds, config.ExtraCommands...)
	return cmds
}

func containsConstellation(values []Constellation, wanted Constellation) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
