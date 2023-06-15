package generic

import (
	"github.com/golang/glog"
	"github.com/openshift/linuxptp-daemon/pkg/plugin"
	ptpv1 "github.com/openshift/ptp-operator/api/v1"
)

func onPTPConfigChangeGeneric(*interface{}, *ptpv1.PtpProfile) error {
	glog.Infof("calling onPTPConfigChangeGeneric")
	return nil
}

func PopulateHwConfigGeneric(*interface{}, *[]ptpv1.HwConfig) error {
	return nil
}

func AfterRunPTPCommandGeneric(*interface{}, *ptpv1.PtpProfile, string) error {
	return nil
}

func Reference(name string) (*plugin.Plugin, *interface{}) {
	if name != "reference" {
		glog.Errorf("Plugin must be initialized as 'reference'")
		return nil, nil
	}
	_plugin := plugin.Plugin{Name: "reference",
		OnPTPConfigChange:  onPTPConfigChangeGeneric,
		PopulateHwConfig:   PopulateHwConfigGeneric,
		AfterRunPTPCommand: AfterRunPTPCommandGeneric,
	}
	return &_plugin, nil
}
