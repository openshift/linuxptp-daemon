package mapping

import (
	"github.com/openshift/linuxptp-daemon/addons/generic"
	"github.com/openshift/linuxptp-daemon/addons/intel"
	plugin2 "github.com/openshift/linuxptp-daemon/addons/plugin"
	"github.com/openshift/linuxptp-daemon/pkg/plugin"
)

// PluginMapping is a map of plugin names to their respective constructors
var PluginMapping = map[string]plugin.New{
	"reference":  generic.Reference,
	plugin2.E810: intel.E810,
}
