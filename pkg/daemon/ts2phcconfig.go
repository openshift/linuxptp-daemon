package daemon

import (
	"errors"
	"fmt"
	"strings"
)

type ts2phcConfSection struct {
	options map[string]string
}

type ts2phcConf struct {
	sections    map[string]ts2phcConfSection
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
	var currentSection string
	conf.sections = make(map[string]ts2phcConfSection)

	for _, line := range lines {
		if strings.HasPrefix(line, "[") {
			currentSection = line
			currentLine := strings.Split(line, "]")

			if len(currentLine) < 2 {
				return errors.New("Section missing closing ']'")
			}

			currentSection = fmt.Sprintf("%s]", currentLine[0])
			section := ts2phcConfSection{options: map[string]string{}}
			conf.sections[currentSection] = section
		} else if currentSection != "" {
			split := strings.IndexByte(line, ' ')
			if split > 0 {
				section := conf.sections[currentSection]
				section.options[line[:split]] = line[split:]
				conf.sections[currentSection] = section
			}
		} else {
			return errors.New("Config option not in section")
		}
	}
	_, exist := conf.sections["[global]"]
	if !exist {
		conf.sections["[global]"] = ts2phcConfSection{options: map[string]string{}}
	}
	return nil
}

func (conf *ts2phcConf) renderts2PhcConf() (string, string) {
	configOut := fmt.Sprintf("#profile: %s\n", conf.profileName)
	conf.mapping = nil

	for name, section := range conf.sections {
		configOut = fmt.Sprintf("%s\n %s", configOut, name)
		if name != "[global]" && name != "[nmea]" {
			iface := name
			iface = strings.ReplaceAll(iface, "[", "")
			iface = strings.ReplaceAll(iface, "]", "")
			conf.mapping = append(conf.mapping, iface)
		}
		for k, v := range section.options {
			configOut = fmt.Sprintf("%s\n %s %s", configOut, k, v)
		}
	}
	return configOut, strings.Join(conf.mapping, ",")
}
