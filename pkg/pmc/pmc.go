package pmc

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/golang/glog"
	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

var (
	cmdGetParentDataSet          = "GET PARENT_DATA_SET"
	cmdGetGMSettings             = "GET GRANDMASTER_SETTINGS_NP"
	cmdSetGMSettings             = "SET GRANDMASTER_SETTINGS_NP"
	cmdGetExternalGMPropertiesNP = "GET EXTERNAL_GRANDMASTER_PROPERTIES_NP"
	cmdSetExternalGMPropertiesNP = "SET EXTERNAL_GRANDMASTER_PROPERTIES_NP"
	cmdGetTimePropertiesDS       = "GET TIME_PROPERTIES_DATA_SET"
	cmdGetCurrentDS              = "GET CURRENT_DATA_SET"
	cmdTimeout                   = 2 * time.Second
	pollTimeout                  = 3 * time.Second
	montiorStartTimeout          = time.Minute
	numRetry                     = 6
	pmcCmdConstPart              = "pmc -u -b 0 -f /var/run/"
	grandmasterSettingsNPRegExp  = regexp.MustCompile((&protocol.GrandmasterSettings{}).RegEx())
	parentDataSetRegExp          = regexp.MustCompile((&protocol.ParentDataSet{}).RegEx())
	externalGMPropertiesNPRegExp = regexp.MustCompile((&protocol.ExternalGrandmasterProperties{}).RegEx())
	timePropertiesDSRegExp       = regexp.MustCompile((&protocol.TimePropertiesDS{}).RegEx())
	currentDSRegExp              = regexp.MustCompile((&protocol.CurrentDS{}).RegEx())
	subscribedEventsRegExp       = regexp.MustCompile((&protocol.SubscribedEvents{}).RegEx())
)

// RunPMCExp ... go expect to run PMC util cmd
func RunPMCExp(configFileName, cmdStr string, promptRE *regexp.Regexp) (result string, matches []string, err error) {
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return "", []string{}, err
	}
	defer utils.CloseExpect(e, r)

	if err = e.Send(cmdStr + "\n"); err == nil {
		result, matches, err = e.Expect(promptRE, cmdTimeout)
		if err != nil {
			glog.Errorf("pmc result match error %s", err)
			return
		}
		glog.Infof("pmc result: %s", result)
	}
	return
}

// RunPMCExpGetGMSettings ... get current GRANDMASTER_SETTINGS_NP
func RunPMCExpGetGMSettings(configFileName string) (g protocol.GrandmasterSettings, err error) {
	cmdStr := cmdGetGMSettings
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return g, err
	}
	defer utils.CloseExpect(e, r)

	for i := 0; i < numRetry; i++ {
		if err = e.Send(cmdStr + "\n"); err == nil {
			result, matches, err1 := e.Expect(grandmasterSettingsNPRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return g, err1
			}
			glog.Infof("pmc result: %s", result)
			for i, m := range matches[1:] {
				g.Update(g.Keys()[i], m)
			}
			break
		}
	}
	return
}

// RunPMCExpSetGMSettings ... set GRANDMASTER_SETTINGS_NP
func RunPMCExpSetGMSettings(configFileName string, g protocol.GrandmasterSettings) (err error) {
	cmdStr := cmdSetGMSettings
	cmdStr += strings.Replace(g.String(), "\n", " ", -1)
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return err
	}
	defer utils.CloseExpect(e, r)

	if err = e.Send(cmdStr + "\n"); err == nil {
		result, _, err1 := e.Expect(grandmasterSettingsNPRegExp, cmdTimeout)
		if err1 != nil {
			glog.Errorf("pmc result match error %v", err1)
			return err1
		}
		glog.Infof("pmc result: %s", result)
	}
	return
}

// RunPMCExpGetParentDS ... GET
func RunPMCExpGetParentDS(configFileName string) (p protocol.ParentDataSet, err error) {
	cmdStr := cmdGetParentDataSet
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return p, err
	}
	defer utils.CloseExpect(e, r)

	for i := 0; i < numRetry; i++ {
		glog.Infof("%s retry %d", cmdGetParentDataSet, i)
		if err = e.Send(cmdStr + "\n"); err == nil {
			result, matches, err1 := e.Expect(parentDataSetRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return p, err1
			}
			glog.Infof("pmc result: %s", result)
			for i, m := range matches[1:] {
				p.Update(p.Keys()[i], m)
			}
			break
		}
	}
	return
}

// RunPMCExpGetExternalGMPropertiesNP ... get current EXTERNAL_GRANDMASTER_PROPERTIES_NP
func RunPMCExpGetExternalGMPropertiesNP(configFileName string) (egp protocol.ExternalGrandmasterProperties, err error) {
	cmdStr := cmdGetExternalGMPropertiesNP
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return
	}
	defer utils.CloseExpect(e, r)

	for i := 0; i < numRetry; i++ {
		if err = e.Send(cmdStr + "\n"); err == nil {
			result, matches, err1 := e.Expect(externalGMPropertiesNPRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return egp, err1
			}
			glog.Infof("pmc result: %s", result)
			for j, m := range matches[1:] {
				egp.Update(egp.Keys()[j], m)
			}
			break
		}
	}
	return
}

// RunPMCExpSetExternalGMPropertiesNP ... set EXTERNAL_GRANDMASTER_PROPERTIES_NP
func RunPMCExpSetExternalGMPropertiesNP(configFileName string, egp protocol.ExternalGrandmasterProperties) (err error) {
	cmdStr := cmdSetExternalGMPropertiesNP
	cmdStr += strings.Replace(egp.String(), "\n", " ", -1)
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("Sending %s %s", pmcCmd, cmdStr)

	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return err
	}
	defer utils.CloseExpect(e, r)

	if err = e.Send(cmdStr + "\n"); err == nil {
		result, dbg, err1 := e.Expect(externalGMPropertiesNPRegExp, cmdTimeout)
		if err1 != nil {
			glog.Errorf("pmc result match error %v", err1)
			glog.Errorf("pmc result: %s", dbg)
			return err1
		}
		glog.Infof("pmc result: %s", result)
	}
	return
}

// RunPMCExpGetTimePropertiesDS ... "GET TIME_PROPERTIES_DATA_SET"
func RunPMCExpGetTimePropertiesDS(configFileName string) (tp protocol.TimePropertiesDS, err error) {
	cmdStr := cmdGetTimePropertiesDS
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return
	}
	defer utils.CloseExpect(e, r)

	for i := 0; i < numRetry; i++ {
		if err = e.Send(cmdStr + "\n"); err == nil {
			_, matches, err1 := e.Expect(timePropertiesDSRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return tp, err1
			}
			for j, m := range matches[1:] {
				tp.Update(tp.Keys()[j], m)
			}
			glog.Infof("pmc result: %++v", tp)
			break
		}
	}
	return
}

// RunPMCExpGetCurrentDS ... "GET CURRENT_DATA_SET"
func RunPMCExpGetCurrentDS(configFileName string) (cds protocol.CurrentDS, err error) {
	cmdStr := cmdGetCurrentDS
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return
	}
	defer utils.CloseExpect(e, r)

	for i := 0; i < numRetry; i++ {
		if err = e.Send(cmdStr + "\n"); err == nil {
			_, matches, err1 := e.Expect(currentDSRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return cds, err1
			}
			for j, m := range matches[1:] {
				cds.Update(cds.Keys()[j], m)
			}
			glog.Infof("pmc result: %++v", cds)
			break
		}
	}
	return
}

// RunPMCGetParentDS runs PMC in non-interactive mode to get PARENT_DATA_SET
// This function does not use the expect package and handles regex parsing separately
func RunPMCGetParentDS(configFileName string) (p protocol.ParentDataSet, err error) {
	cmdStr := cmdGetParentDataSet
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)

	// Run PMC command in non-interactive mode
	cmd := exec.Command("pmc", "-u", "-b", "0", "-f", "/var/run/"+configFileName, cmdStr)

	output, cmdErr := cmd.CombinedOutput()
	if cmdErr != nil {
		glog.Errorf("pmc command execution error: %v", cmdErr)
		return p, cmdErr
	}

	// Convert output to string and apply regex parsing
	result := string(output)
	glog.Infof("pmc result: %s", result)

	// Apply regex parsing separately
	matches := parentDataSetRegExp.FindStringSubmatch(result)
	if len(matches) < 2 {
		glog.Errorf("pmc result regex match failed, no matches found")
		return p, fmt.Errorf("failed to parse PMC output")
	}

	// Parse the matched groups (skip first match which is the full string)
	for j, m := range matches[1:] {
		if j < len(p.Keys()) {
			p.Update(p.Keys()[j], m)
		}
	}

	return p, nil
}

// ParentTimeCurrentDS holds the results from multiple PMC commands
type ParentTimeCurrentDS struct {
	ParentDataSet    protocol.ParentDataSet
	TimePropertiesDS protocol.TimePropertiesDS
	CurrentDS        protocol.CurrentDS
}

// RunPMCExpGetParentTimeAndCurrentDataSets runs PMC in interactive mode and sends three commands sequentially:
// GET PARENT_DATA_SET, GET TIME_PROPERTIES_DATA_SET, and GET CURRENT_DATA_SET
// This is more efficient than spawning separate PMC processes for each command
func RunPMCExpGetParentTimeAndCurrentDataSets(configFileName string) (results ParentTimeCurrentDS, err error) {
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s - running multiple commands", pmcCmd)

	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return results, err
	}
	defer utils.CloseExpect(e, r)

	// Command 1: GET PARENT_DATA_SET
	parentDS, err := getParentDS(e)
	if err != nil {
		return results, err
	}
	results.ParentDataSet = *parentDS

	// Command 2: GET TIME_PROPERTIES_DATA_SET
	timePropertiesDS, err := getTimePropertiesDS(e)
	if err != nil {
		return results, err
	}
	results.TimePropertiesDS = *timePropertiesDS

	// Command 3: GET CURRENT_DATA_SET
	currentDS, err := getCurrentDS(e)
	if err != nil {
		return results, err
	}
	results.CurrentDS = *currentDS

	return results, nil
}

// RunPMCExpGetTimeAndCurrentDataSets runs PMC in interactive mode and sends three commands sequentially:
// GET TIME_PROPERTIES_DATA_SET, and GET CURRENT_DATA_SET
// This is more efficient than spawning separate PMC processes for each command
func RunPMCExpGetTimeAndCurrentDataSets(configFileName string) (results ParentTimeCurrentDS, err error) {
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s - running multiple commands", pmcCmd)

	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return results, err
	}
	defer utils.CloseExpect(e, r)

	// Command 2: GET TIME_PROPERTIES_DATA_SET
	timePropertiesDS, err := getTimePropertiesDS(e)
	if err != nil {
		return results, err
	}
	results.TimePropertiesDS = *timePropertiesDS

	// Command 3: GET CURRENT_DATA_SET
	currentDS, err := getCurrentDS(e)
	if err != nil {
		return results, err
	}
	results.CurrentDS = *currentDS

	return results, nil
}

func getParentDS(exp *expect.GExpect) (*protocol.ParentDataSet, error) {
	results := &protocol.ParentDataSet{}

	glog.Infof("Sending command: %s", cmdGetParentDataSet)
	if err := exp.Send(cmdGetParentDataSet + "\n"); err != nil {
		return results, fmt.Errorf("failed to send PARENT_DATA_SET command: %v", err)
	}

	matched, matches, err := exp.Expect(parentDataSetRegExp, cmdTimeout)
	if err != nil {
		glog.Errorf("PARENT_DATA_SET result match error: %v", err)
		return results, fmt.Errorf("failed to parse PARENT_DATA_SET output: %v", err)
	}
	glog.Infof("PARENT_DATA_SET result: %s", matched)
	return protocol.ProcessMessage[protocol.ParentDataSet](matches)
}

func getTimePropertiesDS(exp *expect.GExpect) (*protocol.TimePropertiesDS, error) {
	results := &protocol.TimePropertiesDS{}
	glog.Infof("Sending command: %s", cmdGetTimePropertiesDS)
	if err := exp.Send(cmdGetTimePropertiesDS + "\n"); err != nil {
		return results, fmt.Errorf("failed to send TIME_PROPERTIES_DATA_SET command: %v", err)
	}

	matched, matches, err := exp.Expect(timePropertiesDSRegExp, cmdTimeout)
	if err != nil {
		glog.Errorf("TIME_PROPERTIES_DATA_SET result match error: %v", err)
		return results, fmt.Errorf("failed to parse TIME_PROPERTIES_DATA_SET output: %v", err)
	}
	glog.Infof("TIME_PROPERTIES_DATA_SET result: %s", matched)
	return protocol.ProcessMessage[protocol.TimePropertiesDS](matches)
}

func getCurrentDS(exp *expect.GExpect) (*protocol.CurrentDS, error) {
	results := &protocol.CurrentDS{}

	glog.Infof("Sending command: %s", cmdGetCurrentDS)
	if err := exp.Send(cmdGetCurrentDS + "\n"); err != nil {
		return results, fmt.Errorf("failed to send CURRENT_DATA_SET command: %v", err)
	}

	matched, matches, err := exp.Expect(currentDSRegExp, cmdTimeout)
	if err != nil {
		glog.Errorf("CURRENT_DATA_SET result match error: %v", err)
		return results, fmt.Errorf("failed to parse CURRENT_DATA_SET output: %v", err)
	}
	glog.Infof("CURRENT_DATA_SET result: %s", matched)
	return protocol.ProcessMessage[protocol.CurrentDS](matches)
}

func getSubcribeEvents(exp *expect.GExpect) (*protocol.SubscribedEvents, error) {
	err := exp.Send("GET SUBSCRIBE_EVENTS_NP\n")
	if err != nil {
		return nil, err
	}
	_, matches, err := exp.Expect(subscribedEventsRegExp, pollTimeout)
	if err != nil {
		return nil, err
	}
	return protocol.ProcessMessage[protocol.SubscribedEvents](matches)
}

// GetPMCMontior spawns and initializes a PMC monitoring process.
func GetPMCMontior(configFileName string) (*expect.GExpect, <-chan error, error) {
	timeout := time.After(10 * montiorStartTimeout)
	for {
		cmd := pmcCmdConstPart + configFileName
		glog.Errorf("Spawning process '%s' for monitoring pmc", cmd)
		exp, r, err := expect.Spawn(cmd, -1)
		if err != nil {
			glog.Errorf("Failed to spawn moniotring pmc process")
		}
		select {
		case <-timeout:
			return exp, r, fmt.Errorf("timed out waiting for pmc to start")
		case <-r:
			return exp, r, fmt.Errorf("pmc need to be restarted")
		default:
			if _, subscribeErr := getSubcribeEvents(exp); subscribeErr != nil {
				continue
			}
			return exp, r, nil
		}
	}
}

// GetMonitorRegex returns a regex pattern for monitoring PMC events based on configuration.
func GetMonitorRegex(monitorParentData bool) *regexp.Regexp {
	parts := make([]string, 0)
	// TODO: Find port state message and make regex
	// if pmc.monitorPortState {
	// 		parts = append(parts, )
	// }

	// TODO: find PMC TimeSync message and make regex
	// if pmc.monitorTimeSync {
	// 		parts = append(parts, )
	// }

	if monitorParentData {
		parts = append(parts, parentDataSetRegExp.String())
	}
	return regexp.MustCompile(`(?m).* seq \d+ RESPONSE MANAGEMENT .*\s*` + strings.Join(parts, `|`))
}
