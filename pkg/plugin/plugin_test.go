package plugin

import (
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestOnPTPConfigChangeWithHardwareConfigOnlyRunsCompatiblePlugins(t *testing.T) {
	called := make(map[string]bool)
	pm := PluginManager{
		Plugins: map[string]*Plugin{
			"hardware": {
				OnPTPConfigChange: func(_ *interface{}, _ *ptpv1.PtpProfile) error {
					called["hardware"] = true
					return nil
				},
			},
			"phc-first-step": {
				RunsWithHardwareConfig: true,
				OnPTPConfigChange: func(_ *interface{}, _ *ptpv1.PtpProfile) error {
					called["phc-first-step"] = true
					return nil
				},
			},
		},
		Data: map[string]*interface{}{},
	}

	assert.Empty(t, pm.OnPTPConfigChangeWithHardwareConfig(&ptpv1.PtpProfile{}))
	assert.False(t, called["hardware"])
	assert.True(t, called["phc-first-step"])
}

func TestOnPTPConfigChangeWithoutHardwareConfigOnlyRunsLegacyPlugins(t *testing.T) {
	called := make(map[string]bool)
	pm := PluginManager{
		Plugins: map[string]*Plugin{
			"hardware": {
				OnPTPConfigChange: func(_ *interface{}, _ *ptpv1.PtpProfile) error {
					called["hardware"] = true
					return nil
				},
			},
			"phc-first-step": {
				RunsWithHardwareConfig: true,
				OnPTPConfigChange: func(_ *interface{}, _ *ptpv1.PtpProfile) error {
					called["phc-first-step"] = true
					return nil
				},
			},
		},
		Data: map[string]*interface{}{},
	}

	assert.Empty(t, pm.OnPTPConfigChangeWithoutHardwareConfig(&ptpv1.PtpProfile{}))
	assert.True(t, called["hardware"])
	assert.False(t, called["phc-first-step"])
}
