package mapping

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/addons/generic"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/addons/intel"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/addons/phcsyncworkaround"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/plugin"
)

var PluginMapping = map[string]plugin.New{
	"reference":           generic.Reference,
	"ntpfailover":         generic.NtpFailover,
	"phc-sync-workaround": phcsyncworkaround.New,
	"e810":                intel.E810,
	"e825":                intel.E825,
	"e830":                intel.E830,
}
