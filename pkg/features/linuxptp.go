package features

import "github.com/golang/glog"

// Versions of linuxptp we compare too
const (
	linuxPTPVersion3112 = "3.1.1-2.el8_6.3"
	linuxPTPVersion3116 = "3.1.1-6.el9_2.7"
	linuxPTPVersion422  = "4.2-2.el9_4.3"
	linuxPTPVersion441  = "4.4-1.el9"
)

// Comparible versions the semver version we compare to
var (
	VersionLinuxPTP3112 = mustGetSemver(linuxPTPVersion3112)
	VersionLinuxPTP3116 = mustGetSemver(linuxPTPVersion3116)
	VersionLinuxPTP422  = mustGetSemver(linuxPTPVersion422)
	VersionLinuxPTP441  = mustGetSemver(linuxPTPVersion441)
)

func getLinuxPTPFeatures(versionStr string) *Features {
	res := &Features{}
	version, err := getSemver(versionStr)
	if err != nil {
		glog.Fatalf("Failed to parse ptp version '%s'", versionStr)
	}

	// Check if version is >= 3.1.1-2.el8_6.3
	if version.Compare(VersionLinuxPTP3112) >= 0 {
		res.OC.Enabled = true
		res.BC.Enabled = true
		res.BC.DualPort = true
	}

	// Check if version >= 3.1.1-6.el9_2.7
	if version.Compare(VersionLinuxPTP3116) >= 0 {
		res.GM.Enabled = true
	}

	// Check if version >= 4.2-2.el9_4.3
	if version.Compare(VersionLinuxPTP422) >= 0 {
		res.LogSeverity = true
		res.GM.HoldOver = true
		res.GM.SyncE = true
		res.BC.PTPHA = true
	}

	// Check if version >= 4.4-1.el9
	if version.Compare(VersionLinuxPTP441) >= 0 {
		res.OC.DualPort = true
		res.BC.HoldOver = true
	}
	return res
}
