package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/openshift/linuxptp-daemon/pkg/config"
	"github.com/openshift/linuxptp-daemon/pkg/event"

	"github.com/golang/glog"

	ptpv1 "github.com/openshift/ptp-operator/api/v1"
)

// predefined config section names
const (
	GlobalSectionName  = "[global]"
	NmeaSectionName    = "[nmea]"
	UnicastSectionName = "[unicast_master_table]"
)

// LinuxPTPUpdate controls whether to update linuxPTP conf
// and contains linuxPTP conf to be updated. It's rendered
// and passed to linuxptp instance by daemon.
type LinuxPTPConfUpdate struct {
	UpdateCh               chan bool
	NodeProfiles           []ptpv1.PtpProfile
	appliedNodeProfileJson []byte
	defaultPTP4lConfig     []byte
}

type ptp4lConfOption struct {
	key   string
	value string
}

type ptp4lConfSection struct {
	sectionName string
	options     []ptp4lConfOption
}

// Ptp4lConf is a structure to represent a parsed ptpconfig,
// which can then be rendered to a string again.
type Ptp4lConf struct {
	sections         []ptp4lConfSection
	profile_name     string
	clock_type       event.ClockType
	gnss_serial_port string // gnss serial port
}

func (conf *Ptp4lConf) getPtp4lConfOptionOrEmptyString(sectionName string, key string) (string, bool) {
	for _, section := range conf.sections {
		if section.sectionName == sectionName {
			for _, option := range section.options {
				if option.key == key {
					return option.value, true
				}
			}
		}
	}

	return "", false
}

func (conf *Ptp4lConf) setPtp4lConfOption(sectionName string, key string, value string, overwrite bool) {
	var updatedSection ptp4lConfSection
	index := -1
	for i, section := range conf.sections {
		if section.sectionName == sectionName {
			updatedSection = section
			index = i
		}
	}
	if index < 0 {
		newSectionOptions := make([]ptp4lConfOption, 0)
		updatedSection = ptp4lConfSection{options: newSectionOptions, sectionName: sectionName}
		index = len(conf.sections)
		conf.sections = append(conf.sections, updatedSection)
	}

	//Stop now if initializing section without option
	if key == "" {
		return
	}
	found := false
	if overwrite {
		for i := range updatedSection.options {
			if updatedSection.options[i].key == key {
				updatedSection.options[i] = ptp4lConfOption{key: key, value: value}
				found = true
			}
		}
	}
	// Append unless already overwrote it.
	if !found {
		updatedSection.options = append(updatedSection.options, ptp4lConfOption{key: key, value: value})
	}

	//Update section in conf
	conf.sections[index] = updatedSection
}

func NewLinuxPTPConfUpdate() (*LinuxPTPConfUpdate, error) {
	if _, err := os.Stat(PTP4L_CONF_FILE_PATH); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("ptp.conf file doesn't exist")
		} else {
			return nil, fmt.Errorf("unknow error searching for the %s file: %v", PTP4L_CONF_FILE_PATH, err)
		}
	}

	defaultPTP4lConfig, err := os.ReadFile(PTP4L_CONF_FILE_PATH)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %v", PTP4L_CONF_FILE_PATH, err)
	}

	return &LinuxPTPConfUpdate{UpdateCh: make(chan bool), defaultPTP4lConfig: defaultPTP4lConfig}, nil
}

func (l *LinuxPTPConfUpdate) UpdateConfig(nodeProfilesJson []byte) error {
	if bytes.Equal(l.appliedNodeProfileJson, nodeProfilesJson) {
		return nil
	}
	if nodeProfiles, ok := tryToLoadConfig(nodeProfilesJson); ok {
		glog.Info("load profiles")
		l.appliedNodeProfileJson = nodeProfilesJson
		l.NodeProfiles = nodeProfiles
		l.UpdateCh <- true

		return nil
	}

	if nodeProfiles, ok := tryToLoadOldConfig(nodeProfilesJson); ok {
		// Support empty old config
		// '{"name":null,"interface":null}'
		if nodeProfiles[0].Name == nil || nodeProfiles[0].Interface == nil {
			glog.Infof("Skip no profile %+v", nodeProfiles[0])
			return nil
		}

		glog.Info("load profiles using old method")
		l.appliedNodeProfileJson = nodeProfilesJson
		l.NodeProfiles = nodeProfiles
		l.UpdateCh <- true

		return nil
	}

	return fmt.Errorf("unable to load profile config")
}

// Try to load the multiple policy config
func tryToLoadConfig(nodeProfilesJson []byte) ([]ptpv1.PtpProfile, bool) {
	ptpConfig := []ptpv1.PtpProfile{}
	err := json.Unmarshal(nodeProfilesJson, &ptpConfig)
	if err != nil {
		return nil, false
	}

	return ptpConfig, true
}

// For backward compatibility we also try to load the one policy scenario
func tryToLoadOldConfig(nodeProfilesJson []byte) ([]ptpv1.PtpProfile, bool) {
	ptpConfig := &ptpv1.PtpProfile{}
	err := json.Unmarshal(nodeProfilesJson, ptpConfig)
	if err != nil {
		return nil, false
	}

	return []ptpv1.PtpProfile{*ptpConfig}, true
}

// PopulatePtp4lConf takes as input a PtpProfile.Ptp4lConf string and outputs as ptp4lConf struct
func (conf *Ptp4lConf) PopulatePtp4lConf(config *string) error {
	var currentSectionName string
	conf.sections = make([]ptp4lConfSection, 0)
	hasSlaveConfigDefined := false
	ifaceCount := 0
	if config != nil {
		for _, line := range strings.Split(*config, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") {
				continue
			} else if strings.HasPrefix(line, "[") {
				currentLine := strings.Split(line, "]")
				if len(currentLine) < 2 {
					return errors.New("Section missing closing ']': " + line)
				}
				currentSectionName = fmt.Sprintf("%s]", currentLine[0])
				if currentSectionName != GlobalSectionName && currentSectionName != NmeaSectionName && currentSectionName != UnicastSectionName {
					ifaceCount++
				}
				conf.setPtp4lConfOption(currentSectionName, "", "", false)
			} else if currentSectionName != "" {
				split := strings.IndexByte(line, ' ')
				if split > 0 {
					key := line[:split]
					value := strings.TrimSpace(line[split:])
					conf.setPtp4lConfOption(currentSectionName, key, value, false)
					if (key == "masterOnly" && value == "0" && currentSectionName != GlobalSectionName) ||
						(key == "serverOnly" && value == "0") ||
						(key == "slaveOnly" && value == "1") ||
						(key == "clientOnly" && value == "1") {
						hasSlaveConfigDefined = true
					}
				}
			} else {
				return errors.New("Config option not in section: " + line)
			}
		}
	}

	if !hasSlaveConfigDefined {
		// No Slave Interfaces defined
		conf.clock_type = event.GM
	} else if ifaceCount > 1 {
		// Multiple interfaces with at least one slave Interface defined
		conf.clock_type = event.BC
	} else {
		// Single slave Interface defined
		conf.clock_type = event.OC
	}

	return nil
}

// ExtendGlobalSection extends Ptp4lConf struct with fields not from ptp4lConf
func (conf *Ptp4lConf) ExtendGlobalSection(profileName string, messageTag string, socketPath string, pProcess string) {
	conf.profile_name = profileName
	conf.setPtp4lConfOption(GlobalSectionName, "message_tag", messageTag, true)
	if socketPath != "" {
		conf.setPtp4lConfOption(GlobalSectionName, "uds_address", socketPath, true)
	}
	if gnssSerialPort, ok := conf.getPtp4lConfOptionOrEmptyString(GlobalSectionName, "ts2phc.nmea_serialport"); ok {
		conf.gnss_serial_port = strings.TrimSpace(gnssSerialPort)
		conf.setPtp4lConfOption(GlobalSectionName, "ts2phc.nmea_serialport", GPSPIPE_SERIALPORT, true)
	}
	if _, ok := conf.getPtp4lConfOptionOrEmptyString(GlobalSectionName, "leapfile"); ok || pProcess == ts2phcProcessName { // not required to check process if leapfile is always included
		conf.setPtp4lConfOption(GlobalSectionName, "leapfile", fmt.Sprintf("%s/%s", config.DefaultLeapConfigPath, os.Getenv("NODE_NAME")), true)
	}
}

// AddInterfaceSection adds interface to Ptp4lConf
func (conf *Ptp4lConf) AddInterfaceSection(iface string) {
	ifaceSectionName := fmt.Sprintf("[%s]", iface)
	conf.setPtp4lConfOption(ifaceSectionName, "", "", false)
}

func getSource(isTs2phcMaster string) event.EventSource {
	if ts2phcMaster, err := strconv.ParseBool(strings.TrimSpace(isTs2phcMaster)); err == nil {
		if ts2phcMaster {
			return event.GNSS
		}
	}
	return event.PPS
}

// RenderPtp4lConf outputs ptp4l config as string
func (conf *Ptp4lConf) RenderPtp4lConf() (configOut string, ifaces config.IFaces) {
	configOut = fmt.Sprintf("#profile: %s\n", conf.profile_name)
	var nmea_source event.EventSource

	for _, section := range conf.sections {
		configOut = fmt.Sprintf("%s\n%s", configOut, section.sectionName)

		if section.sectionName == NmeaSectionName {
			if source, ok := conf.getPtp4lConfOptionOrEmptyString(section.sectionName, "ts2phc.master"); ok {
				nmea_source = getSource(source)
			}
		}
		if section.sectionName != GlobalSectionName && section.sectionName != NmeaSectionName && section.sectionName != UnicastSectionName {
			i := section.sectionName
			i = strings.ReplaceAll(i, "[", "")
			i = strings.ReplaceAll(i, "]", "")
			iface := config.Iface{Name: i}
			if source, ok := conf.getPtp4lConfOptionOrEmptyString(section.sectionName, "ts2phc.master"); ok {
				iface.Source = getSource(source)
			} else {
				// if not defined here, use source defined at nmea section
				iface.Source = nmea_source
			}
			if masterOnly, ok := conf.getPtp4lConfOptionOrEmptyString(section.sectionName, "masterOnly"); ok {
				// TODO add error handling
				iface.IsMaster, _ = strconv.ParseBool(strings.TrimSpace(masterOnly))
			}
			ifaces = append(ifaces, config.Iface{
				Name:   iface.Name,
				Source: iface.Source,
				PhcId:  iface.PhcId,
			})
		}
		for _, option := range section.options {
			k := option.key
			v := option.value
			configOut = fmt.Sprintf("%s\n%s %s", configOut, k, v)
		}
	}
	return configOut, ifaces
}
