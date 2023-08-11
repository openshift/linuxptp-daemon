package pmc

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/golang/glog"
	"github.com/openshift/linuxptp-daemon/pkg/protocol"
)

var (
	ClockClassChangeRegEx = regexp.MustCompile(`gm.ClockClass[[:space:]]+(\d+)`)
	ClockClassUpdateRegEx = regexp.MustCompile(`clockClass[[:space:]]+(\d+)`)
	GetGMSettingsRegEx    = regexp.MustCompile(`clockClass[[:space:]]+(\d+)[[:space:]]+clockAccuracy[[:space:]]+(0x\d+)`)
	CmdGetParentDataSet   = "GET PARENT_DATA_SET"
	CmdGetGMSettings      = "GET GRANDMASTER_SETTINGS_NP"
	CmdSetGMSettings      = "SET GRANDMASTER_SETTINGS_NP"
)

// RunPMCExp ... go expect to run PMC util cmd
func RunPMCExp(configFileName, cmdStr string, promptRE *regexp.Regexp) (result string, matches []string, err error) {

	configFile := fmt.Sprintf("/var/run/%s", configFileName)
	args := []string{"-u", "-f", configFile, cmdStr}

	glog.Infof("run command: pmc %s", strings.Join(args, " "))
	output, err := exec.Command("pmc", args...).Output()
	if err != nil {
		return "", []string{}, err
	}

	matches = promptRE.FindStringSubmatch(string(output))
	if matches == nil {
		return "", []string{}, fmt.Errorf("pmc result does not match expectations")
	}
	glog.Infof("pmc result: %s", output)
	return
}

// RunPMCExpGetGMSettings ... get current GRANDMASTER_SETTINGS_NP
func RunPMCExpGetGMSettings(configFileName string) (g protocol.GrandmasterSettings, err error) {
	configFile := fmt.Sprintf("/var/run/%s", configFileName)
	if _, err := os.Stat(configFile); err != nil {
		return g, fmt.Errorf("failed to read config file %s", configFile)
	}

	cmdStr := CmdGetGMSettings
	args := []string{"-u", "-f", configFile, cmdStr}

	glog.Infof("run command: pmc %s", strings.Join(args, " "))
	output, err := exec.Command("pmc", args...).Output()
	if err != nil {
		return g, err
	}

	r := regexp.MustCompile(g.RegEx())
	matches := r.FindStringSubmatch(string(output))
	if matches == nil {
		return g, fmt.Errorf("pmc result does not match expectations")
	}
	glog.Infof("pmc result: %s", output)
	for i, m := range matches[1:] {
		g.Update(g.Keys()[i], m)
	}
	return
}

// RunPMCExpSetGMSettings ... set GRANDMASTER_SETTINGS_NP
func RunPMCExpSetGMSettings(configFileName string, g protocol.GrandmasterSettings) (err error) {
	configFile := fmt.Sprintf("/var/run/%s", configFileName)
	if _, err := os.Stat(configFile); err != nil {
		return fmt.Errorf("failed to read file %s", configFile)
	}

	cmdStr := CmdSetGMSettings
	cmdStr += strings.Replace(g.String(), "\n", " ", -1)
	args := []string{"-u", "-f", configFile, cmdStr}

	glog.Infof("run command: pmc %s", strings.Join(args, " "))
	output, err := exec.Command("pmc", args...).Output()
	if err != nil {
		return err
	}

	// make sure the result is in expected format
	r := regexp.MustCompile(g.RegEx())
	matches := r.FindStringSubmatch(string(output))
	if matches == nil {
		return fmt.Errorf("pmc result does not match expectations")
	}
	glog.Infof("pmc result: %s", output)
	return nil
}
