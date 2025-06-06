package features

import (
	"github.com/golang/glog"
)

// Flags feature flags
var Flags *Features

func init() {
	Flags = &Features{}
}

// Features ...
type Features struct {
	OC          OCFeatures
	BC          BCFeatures
	GM          GMFeatures
	LogSeverity bool
}

// Print prints
// out the internal values of feature gflags
func (f Features) Print() {
	f.OC.Print()
	f.BC.Print()
	f.GM.Print()
	glog.Info("LogSeverity: ", f.LogSeverity)
}

// And applies a logical and on the feature sets
func (f Features) And(other Features) *Features {
	// Unfortunalty until we can fully backport the code for all features
	// to all versions we need to also gate based on OPC version.
	// So this should be temporary.
	return &Features{
		OC:          f.OC.And(other.OC),
		BC:          f.BC.And(other.BC),
		GM:          f.GM.And(other.GM),
		LogSeverity: f.LogSeverity && other.LogSeverity,
	}
}

// OCFeatures ...
type OCFeatures struct {
	Enabled  bool
	DualPort bool
}

// Print ...
func (f OCFeatures) Print() {
	glog.Info("OC Enabled: ", f.Enabled)
	glog.Info("OC DualPort: ", f.DualPort)
}

// And applies a logical and on the feature sets
func (f OCFeatures) And(other OCFeatures) OCFeatures {
	return OCFeatures{
		Enabled:  f.Enabled && other.Enabled,
		DualPort: f.DualPort && other.DualPort,
	}
}

// BCFeatures ...
type BCFeatures struct {
	Enabled  bool
	HoldOver bool
	DualPort bool
	PTPHA    bool
}

// Print ...
func (f BCFeatures) Print() {
	glog.Info("BC Enabled: ", f.Enabled)
	glog.Info("BC HoldOver: ", f.HoldOver)
	glog.Info("BC DualPort: ", f.DualPort)
	glog.Info("BC PTPHA: ", f.PTPHA)
}

// And applies a logical and on the feature sets
func (f BCFeatures) And(other BCFeatures) BCFeatures {
	return BCFeatures{
		Enabled:  f.Enabled && other.Enabled,
		HoldOver: f.HoldOver && other.HoldOver,
		DualPort: f.DualPort && other.DualPort,
		PTPHA:    f.PTPHA && other.PTPHA,
	}
}

// GMFeatures ...
type GMFeatures struct {
	Enabled  bool
	HoldOver bool
	SyncE    bool
}

// Print ...
func (f GMFeatures) Print() {
	glog.Info("GM Enabled: ", f.Enabled)
	glog.Info("GM HoldOver: ", f.HoldOver)
	glog.Info("GM SyncE: ", f.SyncE)
}

// And applies a logical and on the feature sets
func (f GMFeatures) And(other GMFeatures) GMFeatures {
	return GMFeatures{
		Enabled:  f.Enabled && other.Enabled,
		HoldOver: f.HoldOver && other.HoldOver,
		SyncE:    f.SyncE && other.SyncE,
	}
}

// SetFlags sets the feature flags based on the linuxptp and ocp versions
func SetFlags(linuxptpVersion, ocpVersion string) {
	Flags = getLinuxPTPFeatures(linuxptpVersion)
	Flags = Flags.And(getOCPFeatures(ocpVersion))
}
