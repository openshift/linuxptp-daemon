package plugin

import (
	ptpv1 "github.com/openshift/ptp-operator/api/v1"
)

type New func(string) (*Plugin, *interface{})
type OnPTPConfigChange func(*interface{}, *ptpv1.PtpProfile) error
type PopulateHwConfig func(*interface{}, *[]ptpv1.HwConfig) error
type AfterRunPTPCommand func(*interface{}, *ptpv1.PtpProfile, string) error

type Plugin struct {
	Name               string
	Options            interface{}
	OnPTPConfigChange  OnPTPConfigChange
	AfterRunPTPCommand AfterRunPTPCommand
	PopulateHwConfig   PopulateHwConfig
}
