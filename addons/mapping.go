package mapping

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/addons/generic"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/addons/intel"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
)

var PluginMapping = map[string]plugin.New{
	"reference":   generic.Reference,
	"ntpfailover": generic.NtpFailover,
	"e810":        intel.E810,
}
