package mapping

import (
	"github.com/josephdrichard/linuxptp-daemon/addons/generic"
	"github.com/josephdrichard/linuxptp-daemon/addons/intel"
	"github.com/josephdrichard/linuxptp-daemon/pkg/plugin"
)

var PluginMapping = map[string]plugin.New{
	"reference": generic.Reference,
	"e810":      intel.E810,
}
