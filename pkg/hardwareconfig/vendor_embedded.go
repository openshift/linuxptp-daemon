package hardwareconfig

import (
	_ "embed"
)

// Embedded hardware-vendor defaults baked into the binary.
// Add new hardware models here as needed.

//go:embed hardware-vendor/intel/e810/defaults.yaml
var intelE810DefaultsYAML []byte

// embeddedDefaults maps hwDefPath -> raw YAML contents.
// Example key: "intel/e810"
var embeddedDefaults = map[string][]byte{
	"intel/e810": intelE810DefaultsYAML,
}
