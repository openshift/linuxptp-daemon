// Package testhelpers provides utility functions for testing.
package testhelpers

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/features"
)

// SetupTests sets the feature flags
func SetupTests() func() {
	features.SetFlags(features.LatestLinuxPTPVersion.String(), features.GetOCPVersion())
	features.Flags.Print()
	return TeardownTests
}

// TeardownTests ...
func TeardownTests() {
	alias.ClearAliases()
}

// SetupForTestOC ...
func SetupForTestOC() (skip bool, teardown func()) {
	return !features.Flags.OC.Enabled, func() {}
}

// SetupForTestOCDualPort ...
func SetupForTestOCDualPort() (skip bool, teardown func()) {
	enabled := features.Flags.OC.Enabled && features.Flags.OC.DualPort
	return !enabled, func() {}
}

// SetupForTestBC ...
func SetupForTestBC() (skip bool, teardown func()) {
	return !features.Flags.BC.Enabled, func() {}
}

// SetupForTestTBC ...
func SetupForTestTBC() (skip bool, teardown func()) {
	enabled := (features.Flags.BC.Enabled && features.Flags.BC.HoldOver)
	return !enabled, func() {}
}

// SetupForTestBCPTPHA ...
func SetupForTestBCPTPHA() (skip bool, teardown func()) {
	enabled := features.Flags.BC.Enabled && features.Flags.BC.PTPHA
	return !enabled, func() {}
}

// SetupForTestBCDualPort ...
func SetupForTestBCDualPort() (skip bool, teardown func()) {
	enabled := features.Flags.BC.Enabled && features.Flags.BC.DualPort
	return !enabled, func() {}
}

// SetupForTestGM ...
func SetupForTestGM() (skip bool, teardown func()) {
	return !features.Flags.GM.Enabled, func() {}
}

// SetupForTestTGM ...
func SetupForTestTGM() (skip bool, teardown func()) {
	enabled := features.Flags.GM.Enabled && features.Flags.GM.HoldOver
	return !enabled, func() {}
}

// SetupForTestGMSyncE ...
func SetupForTestGMSyncE() (skip bool, teardown func()) {
	enabled := features.Flags.GM.Enabled && features.Flags.GM.SyncE
	return !enabled, func() {}
}

// SetupForTestLogSeverity ...
func SetupForTestLogSeverity() (skip bool, teardown func()) {
	return !features.Flags.LogSeverity, func() {}
}
