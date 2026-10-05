package plugin

import (
	"fmt"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// Plugin type
type Plugin struct {
	Name               string
	Options            interface{}
	OnPTPConfigChange  OnPTPConfigChange
	AfterRunPTPCommand AfterRunPTPCommand
	PopulateHwConfig   PopulateHwConfig
	SetEventChannel    SetEventChannel
	ProcessLog         ProcessLog
}

// PluginManager type
type PluginManager struct { //nolint:revive
	Plugins map[string]*Plugin
	Data    map[string]*interface{}
}

// New type
type New func(string) (*Plugin, *interface{})

// OnPTPConfigChange type
type OnPTPConfigChange func(*interface{}, *ptpv1.PtpProfile) error

// PopulateHwConfig type
type PopulateHwConfig func(*interface{}, *[]ptpv1.HwConfig) error

// AfterRunPTPCommand type
type AfterRunPTPCommand func(*interface{}, *ptpv1.PtpProfile, string) error

// SetEventChannel type
type SetEventChannel func(*interface{}, chan<- event.Event)

// ProcessLog type
type ProcessLog func(*interface{}, string, string) string

// OnPTPConfigChange is plugin interface
func (pm *PluginManager) OnPTPConfigChange(nodeProfile *ptpv1.PtpProfile) []error {
	var errs []error
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.OnPTPConfigChange
		if pluginFunc != nil {
			if err := pluginFunc(pm.Data[pluginName], nodeProfile); err != nil {
				profileName := "unknown"
				if nodeProfile.Name != nil {
					profileName = *nodeProfile.Name
				}
				glog.Warningf("Plugin '%s' OnPTPConfigChange failed for profile '%s': %v", pluginName, profileName, err)
				errs = append(errs, fmt.Errorf("plugin %s: %w", pluginName, err))
			}
		}
	}
	return errs
}

// AfterRunPTPCommand is plugin interface
func (pm *PluginManager) AfterRunPTPCommand(nodeProfile *ptpv1.PtpProfile, command string) {
	if nodeProfile == nil {
		return
	}
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.AfterRunPTPCommand
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], nodeProfile, command)
		}
	}
}

// PopulateHwConfig is plugin interface
func (pm *PluginManager) PopulateHwConfig(hwconfigs *[]ptpv1.HwConfig) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.PopulateHwConfig
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], hwconfigs)
		}
	}
}

// SetEventChannel passes the event channel to all plugins that need it.
func (pm *PluginManager) SetEventChannel(ch chan<- event.Event) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.SetEventChannel
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], ch)
		}
	}
}

// ProcessLog is plugin interface
func (pm *PluginManager) ProcessLog(pname string, log string) string {
	ret := log

	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.ProcessLog
		if pluginFunc != nil {
			pluginData := pm.Data
			pluginDataName := pluginData[pluginName]
			ret = pluginFunc(pluginDataName, pname, ret)
		}
	}
	return ret
}
