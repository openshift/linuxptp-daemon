package hardwareconfig

import (
	_ "embed"
)

// Embedded hardware-vendor defaults baked into the binary.
// Add new hardware models here as needed.

//go:embed hardware-vendor/intel/e810/defaults.yaml
var intelE810DefaultsYAML []byte

//go:embed hardware-vendor/intel/e825/defaults.yaml
var intelE825DefaultsYAML []byte

//go:embed hardware-vendor/intel/e825/behavior-profiles.yaml
var intelE825BehaviorProfilesYAML []byte

// embeddedDefaults maps hwDefPath -> raw YAML contents.
// Example key: "intel/e810"
var embeddedDefaults = map[string][]byte{
	"intel/e810": intelE810DefaultsYAML,
	"intel/e825": intelE825DefaultsYAML,
}

// embeddedBehaviorProfiles maps hwDefPath -> raw YAML contents for behavior profiles.
// Example key: "intel/e825"
var embeddedBehaviorProfiles = map[string][]byte{
	"intel/e825": intelE825BehaviorProfilesYAML,
}
