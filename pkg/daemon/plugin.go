package daemon

import (
	"github.com/golang/glog"
	"github.com/josephdrichard/linuxptp-daemon/addons"
	"github.com/josephdrichard/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

type PluginManager struct {
	plugins map[string]*plugin.Plugin
	data    map[string]*interface{}
}

func registerPlugins(plugins []string) PluginManager {
	glog.Infof("Begin plugin registration...")
	manager := PluginManager{plugins: make(map[string]*plugin.Plugin),
		data: make(map[string]*interface{}),
	}
	for _, name := range plugins {
		currentPlugin, currentData := registerPlugin(name)
		if currentPlugin != nil {
			manager.plugins[name] = currentPlugin
			manager.data[name] = currentData
		}
	}
	return manager
}

func registerPlugin(name string) (*plugin.Plugin, *interface{}) {
	glog.Infof("Trying to register plugin: " + name)
	for mName, mConstructor := range mapping.PluginMapping {
		if mName == name {
			return mConstructor(name)
		}
	}
	glog.Errorf("Plugin not found: " + name)
	return nil, nil
}

func (pm *PluginManager) OnPTPConfigChange(nodeProfile *ptpv1.PtpProfile) {
	for pluginName, pluginObject := range pm.plugins {
		pluginObject.OnPTPConfigChange(pm.data[pluginName], nodeProfile)
	}
}

func (pm *PluginManager) AfterRunPTPCommand(nodeProfile *ptpv1.PtpProfile, command string) {
	for pluginName, pluginObject := range pm.plugins {
		pluginObject.AfterRunPTPCommand(pm.data[pluginName], nodeProfile, command)
	}
}

func (pm *PluginManager) PopulateHwConfig(hwconfigs *[]ptpv1.HwConfig) {
	for pluginName, pluginObject := range pm.plugins {
		pluginObject.PopulateHwConfig(pm.data[pluginName], hwconfigs)
	}
}
