package daemon

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/addons"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
)

func registerPlugins(plugins []string) plugin.PluginManager {
	glog.Infof("Begin plugin registration...")
	manager := plugin.PluginManager{Plugins: make(map[string]*plugin.Plugin),
		Data: make(map[string]*interface{}),
	}
	for _, name := range plugins {
		currentPlugin, currentData := registerPlugin(name)
		if currentPlugin != nil {
			manager.Plugins[name] = currentPlugin
			manager.Data[name] = currentData
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
