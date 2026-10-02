package plugin

import (
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestAfterRunPTPCommand_NilProfileDoesNotPanic(t *testing.T) {
	called := false
	pm := PluginManager{
		Plugins: map[string]*Plugin{
			"e810": {
				AfterRunPTPCommand: func(_ *interface{}, nodeProfile *ptpv1.PtpProfile, _ string) error {
					called = true
					_ = nodeProfile.Plugins
					return nil
				},
			},
		},
	}
	assert.NotPanics(t, func() {
		pm.AfterRunPTPCommand(nil, "pmc")
	})
	assert.False(t, called)
}
