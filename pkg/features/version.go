package features

import (
	"fmt"
	"os/exec"
	"strings"

	semver "github.com/Masterminds/semver/v3"
	"github.com/golang/glog"
)

func getSemver(versionStr string) (*semver.Version, error) {
	// Parses a version that looks like the following
	// "3.1.1-2.el8_6.3"
	// "3.1.1-6.el9_2.7"
	// "4.2-2.el9_4.3"
	// "4.4-1.el9"
	//
	// It is similar to semver but the semver doesn't allow "_"
	// in the "pre-release identifier"
	// So we replace the "_" with a dot to make it a valid semver string
	v, err := semver.NewVersion(
		strings.Replace(versionStr, "_", ".", 1),
	)
	return v, err
}

func mustGetSemver(versionStr string) *semver.Version {
	v, err := getSemver(versionStr)
	if err != nil {
		panic(fmt.Sprintf("Invalid Version %s", err))
	}
	return v
}

func runCmd(cmdLine string) string {
	args := strings.Fields(cmdLine)
	cmd := exec.Command(args[0], args[1:]...)
	outBytes, _ := cmd.CombinedOutput()
	return string(outBytes)
}

// GetLinuxPTPPackageVersion returns the version of the installed linuxptp package
func GetLinuxPTPPackageVersion() string {
	// version of installed linuxptp package will look something like 3.1.1-2.el8_6.3
	// note:
	// 	package version is used as ptp4l does not include patch info e.g.
	// 	3.1.1-2.el8_6.3 and 3.1.1-6.el9_2.7 both have `ptp4l -v` versions of 3.1.1
	version := runCmd("rpm -q --queryformat='%{VERSION}-%{Release}' linuxptp")
	version = strings.Trim(version, "'")
	glog.Infof("linuxptp package version is: %s", version)
	return version
}
