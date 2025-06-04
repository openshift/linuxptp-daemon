package testhelpers

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/features"
)

// SetupTests sets the feature flags
func SetupTests() func() {
	features.SetFlags(features.LatestLinuxPTPVersion.String(), features.GetOCPVersion())
	features.Flags.Print()
	return TeardownTests
}

// TeardownTests ...
func TeardownTests() {}
