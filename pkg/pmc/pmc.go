package pmc

import (
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/golang/glog"
	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

var (
	cmdGetParentDataSet          = "GET PARENT_DATA_SET"
	cmdGetGMSettings             = "GET GRANDMASTER_SETTINGS_NP"
	cmdSetGMSettings             = "SET GRANDMASTER_SETTINGS_NP"
	cmdGetExternalGMPropertiesNP = "GET EXTERNAL_GRANDMASTER_PROPERTIES_NP"
	cmdSetExternalGMPropertiesNP = "SET EXTERNAL_GRANDMASTER_PROPERTIES_NP"
	cmdGetTimePropertiesDS       = "GET TIME_PROPERTIES_DATA_SET"
	cmdGetCurrentDS              = "GET CURRENT_DATA_SET"
	cmdTimeout                   = 2000 * time.Millisecond
	sigTimeout                   = 500 * time.Millisecond
	numRetry                     = 6
	pmcCmdConstPart              = "pmc -u -b 0 -f /var/run/"
	grandmasterSettingsNPRegExp  = regexp.MustCompile((&protocol.GrandmasterSettings{}).RegEx())
	parentDataSetRegExp          = regexp.MustCompile((&protocol.ParentDataSet{}).RegEx())
	externalGMPropertiesNPRegExp = regexp.MustCompile((&protocol.ExternalGrandmasterProperties{}).RegEx())
	timePropertiesDSRegExp       = regexp.MustCompile((&protocol.TimePropertiesDS{}).RegEx())
	currentDSRegExp              = regexp.MustCompile((&protocol.CurrentDS{}).RegEx())
)

// RunPMCExp ... go expect to run PMC util cmd
func RunPMCExp(configFileName, cmdStr string, promptRE *regexp.Regexp) (result string, matches []string, err error) {
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return "", []string{}, err
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

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
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

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
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return err
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

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

// RunPMCExpGetParentDS ... GET PARENT_DATA_SET
func RunPMCExpGetParentDS(configFileName string) (p protocol.ParentDataSet, err error) {
	cmdStr := cmdGetParentDataSet
	pmcCmd := pmcCmdConstPart + configFileName
	glog.Infof("%s \"%s\"", pmcCmd, cmdStr)
	e, r, err := expect.Spawn(pmcCmd, -1)
	if err != nil {
		return p, err
	}
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

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
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	for range numRetry {
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
			for i, m := range matches[1:] {
				egp.Update(egp.Keys()[i], m)
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
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

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
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	for range numRetry {
		if err = e.Send(cmdStr + "\n"); err == nil {
			_, matches, err1 := e.Expect(timePropertiesDSRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return tp, err1
			}
			for i, m := range matches[1:] {
				tp.Update(tp.Keys()[i], m)
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
	defer func() {
		e.SendSignal(syscall.SIGTERM)
		for timeout := time.After(sigTimeout); ; {
			select {
			case <-r:
				e.Close()
				return
			case <-timeout:
				e.Send("\x03")
				e.Close()
				return
			}
		}
	}()

	for range numRetry {
		if err = e.Send(cmdStr + "\n"); err == nil {
			_, matches, err1 := e.Expect(currentDSRegExp, cmdTimeout)
			if err1 != nil {
				if _, ok := err1.(expect.TimeoutError); ok {
					continue
				}
				glog.Errorf("pmc result match error %v", err1)
				return cds, err1
			}
			for i, m := range matches[1:] {
				cds.Update(cds.Keys()[i], m)
			}
			glog.Infof("pmc result: %++v", cds)
			break
		}
	}
	return
}
