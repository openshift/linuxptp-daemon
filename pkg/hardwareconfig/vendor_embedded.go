package hardwareconfig

import (
	_ "embed"
)

// Hardware definition path constants used across the package.
const (
	HwDefIntelE810     = "intel/e810"
	HwDefIntelE825     = "intel/e825"
	HwDefDellXR8720t   = "dell/XR8720t"
	HwDefHPEEL140Gen12 = "hpe/EL140-Gen12"
)

// Embedded hardware-vendor defaults baked into the binary.
// Add new hardware models here as needed.

//go:embed hardware-vendor/intel/e810/defaults.yaml
var intelE810DefaultsYAML []byte

//go:embed hardware-vendor/intel/e825/defaults.yaml
var intelE825DefaultsYAML []byte

//go:embed hardware-vendor/dell/XR8720t/defaults.yaml
var dellXR8720tDefaultsYAML []byte

//go:embed hardware-vendor/hpe/EL140-Gen12/defaults.yaml
var hpeEL140Gen12DefaultsYAML []byte

//go:embed hardware-vendor/intel/e825/behavior-profiles.yaml
var intelE825BehaviorProfilesYAML []byte

//go:embed hardware-vendor/intel/e825/delays.yaml
var intelE825DelaysYAML []byte

//go:embed hardware-vendor/dell/XR8720t/behavior-profiles.yaml
var dellXR8720tBehaviorProfilesYAML []byte

//go:embed hardware-vendor/dell/XR8720t/delays.yaml
var dellXR8720tDelaysYAML []byte

//go:embed hardware-vendor/hpe/EL140-Gen12/behavior-profiles.yaml
var hpeEL140Gen12BehaviorProfilesYAML []byte

//go:embed hardware-vendor/hpe/EL140-Gen12/delays.yaml
var hpeEL140Gen12DelaysYAML []byte

// embeddedDefaults maps hwDefPath -> raw YAML contents.
// Example key: "intel/e810"
var embeddedDefaults = map[string][]byte{
	HwDefIntelE810:     intelE810DefaultsYAML,
	HwDefIntelE825:     intelE825DefaultsYAML,
	HwDefDellXR8720t:   dellXR8720tDefaultsYAML,
	HwDefHPEEL140Gen12: hpeEL140Gen12DefaultsYAML,
}

// embeddedBehaviorProfiles maps hwDefPath -> raw YAML contents for behavior profiles.
// Example key: "intel/e825"
var embeddedBehaviorProfiles = map[string][]byte{
	HwDefIntelE825:     intelE825BehaviorProfilesYAML,
	HwDefDellXR8720t:   dellXR8720tBehaviorProfilesYAML,
	HwDefHPEEL140Gen12: hpeEL140Gen12BehaviorProfilesYAML,
}

// embeddedDelays maps hwDefPath -> raw YAML contents for delay compensation models.
// Example key: "intel/e825"
var embeddedDelays = map[string][]byte{
	HwDefIntelE825:     intelE825DelaysYAML,
	HwDefDellXR8720t:   dellXR8720tDelaysYAML,
	HwDefHPEEL140Gen12: hpeEL140Gen12DelaysYAML,
}
