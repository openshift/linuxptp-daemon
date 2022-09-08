package daemon

import (
	"errors"
	"fmt"
	"strings"
)

type ts2phcConfSection struct {
	sectionName string
	options     map[string]string
}

type ts2phcConf struct {
	sections    []ts2phcConfSection
	mapping     []string
	profileName string
}

/*
# export ETH=enp1s0f0
# export TS2PHC_CONFIG=/home/<user>/linuxptp-3.1/configs/ts2phc-generic.cfg
# ts2phc -f $TS2PHC_CONFIG -s generic -m -c $ETH
# cat $TS2PHC_CONFIG
[global]
use_syslog 0
verbose 1
logging_level 7
ts2phc.pulsewidth 100000000
#For GNSS module
#ts2phc.nmea_serialport /dev/ttyGNSS_BBDD_0 #BB bus number DD device number /dev/
ttyGNSS_1800_0
#leapfile /../<path to .list leap second file>
[<network interface>]
ts2phc.extts_polarity rising
*/
func (conf *ts2phcConf) populateTs2PhcConf(config *string) error {
	lines := strings.Split(*config, "\n")
	var currentSectionName string
	var currentSection ts2phcConfSection
	conf.sections = make([]ts2phcConfSection, 0)
	globalIsDefined := false

	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		} else if strings.HasPrefix(line, "[") {
			if currentSectionName != "" {
				conf.sections = append(conf.sections, currentSection)
			}
			currentSectionName = line
			currentLine := strings.Split(line, "]")

			if len(currentLine) < 2 {
				return errors.New("Section missing closing ']'")
			}

			currentSectionName = fmt.Sprintf("%s]", currentLine[0])
			if currentSectionName == "[global]" {
				globalIsDefined = true
			}
			currentSection = ts2phcConfSection{options: map[string]string{}, sectionName: currentSectionName}
		} else if currentSectionName != "" {
			split := strings.IndexByte(line, ' ')
			if split > 0 {
				currentSection.options[line[:split]] = line[split:]
			}
		} else {
			return errors.New("config option not in section")
		}
	}
	if currentSectionName != "" {
		conf.sections = append(conf.sections, currentSection)
	}
	if !globalIsDefined {
		conf.sections = append(conf.sections, ts2phcConfSection{options: map[string]string{}, sectionName: "[global]"})
	}
	return nil
}

func (conf *ts2phcConf) renderTs2PhcConf() string {
	configOut := fmt.Sprintf("#profile: %s\n", conf.profileName)
	conf.mapping = nil

	for _, section := range conf.sections {
		configOut = fmt.Sprintf("%s\n%s", configOut, section.sectionName)
		for k, v := range section.options {
			configOut = fmt.Sprintf("%s\n%s %s", configOut, k, v)
		}
	}
	return configOut
}
