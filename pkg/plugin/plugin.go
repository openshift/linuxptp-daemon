package plugin

import (
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// Plugin type
type Plugin struct {
	Name                   string
	Options                interface{}
	OnPTPConfigChange      OnPTPConfigChange
	AfterRunPTPCommand     AfterRunPTPCommand
	PopulateHwConfig       PopulateHwConfig
	RegisterEnableCallback RegisterEnableCallback
	ProcessLog             ProcessLog
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

// RegisterEnableCallback type
type RegisterEnableCallback func(*interface{}, string, func(bool))

// ProcessLog type
type ProcessLog func(*interface{}, string, string) string

// OnPTPConfigChange is plugin interface
func (pm *PluginManager) OnPTPConfigChange(nodeProfile *ptpv1.PtpProfile) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.OnPTPConfigChange
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], nodeProfile)
		}
	}
}

// AfterRunPTPCommand is plugin interface
func (pm *PluginManager) AfterRunPTPCommand(nodeProfile *ptpv1.PtpProfile, command string) {
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

// RegisterEnableCallback is plugin interface
func (pm *PluginManager) RegisterEnableCallback(pname string, cmdSetEnabled func(bool)) {
	for pluginName, pluginObject := range pm.Plugins {
		pluginFunc := pluginObject.RegisterEnableCallback
		if pluginFunc != nil {
			pluginFunc(pm.Data[pluginName], pname, cmdSetEnabled)
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
