package features_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/features"
)

// clearFeatures ... used by unit tests
func clearFeatures() {
	features.Flags = &features.Features{}
}

func TestLinuxPTPFlags(t *testing.T) {
	cases := []struct {
		versionStr    string
		expectedFlags features.Features
	}{
		{
			features.VersionLinuxPTP3112.String(),
			features.Features{
				OC: features.OCFeatures{
					Enabled: true,
				},
				BC: features.BCFeatures{
					Enabled:  true,
					DualPort: true,
				},
			},
		},
		{
			features.VersionLinuxPTP3116.String(),
			features.Features{
				OC: features.OCFeatures{
					Enabled: true,
				},
				BC: features.BCFeatures{
					Enabled:  true,
					DualPort: true,
				},
				GM: features.GMFeatures{
					Enabled: true,
				},
			},
		},
		{
			features.VersionLinuxPTP422.String(),
			features.Features{
				OC: features.OCFeatures{
					Enabled: true,
				},
				BC: features.BCFeatures{
					Enabled:  true,
					DualPort: true,
					PTPHA:    true,
				},
				GM: features.GMFeatures{
					Enabled:  true,
					HoldOver: true,
					SyncE:    true,
				},
				LogSeverity: true,
			},
		},
		{
			features.VersionLinuxPTP441.String(),
			features.Features{
				OC: features.OCFeatures{
					Enabled:  true,
					DualPort: true,
				},
				BC: features.BCFeatures{
					Enabled:  true,
					DualPort: true,
					PTPHA:    true,
				},
				GM: features.GMFeatures{
					Enabled:  true,
					HoldOver: true,
					SyncE:    true,
				},
				LogSeverity: true,
			},
		},
	}

	for _, c := range cases {
		clearFeatures()
		checkFlags(t, c.versionStr, features.LatestOCPInMatrix, c.expectedFlags)
	}
}

func checkFlags(t *testing.T, linuxptpVersion, ocpVersion string, expected features.Features) {
	features.SetFlags(linuxptpVersion, ocpVersion)
	if features.Flags.OC.Enabled != expected.OC.Enabled {
		t.Errorf("linuxptp version %s, ocpVersion %s OC Enabled should be %v",
			linuxptpVersion,
			ocpVersion,
			expected.OC.Enabled,
		)
	}
	if features.Flags.OC.DualPort != expected.OC.DualPort {
		t.Errorf("linuxptp version %s, ocpVersion %s OC DualPort should be %v",
			linuxptpVersion,
			ocpVersion,
			expected.OC.DualPort,
		)
	}

	if features.Flags.BC.Enabled != expected.BC.Enabled {
		t.Errorf("linuxptp version %s, ocpVersion %s BC Enabled should be %v",
			linuxptpVersion,
			ocpVersion,
			expected.BC.Enabled,
		)
	}
	if features.Flags.BC.DualPort != expected.BC.DualPort {
		t.Errorf("linuxptp version %s, ocpVersion %s BC DualPort should be %v",
			linuxptpVersion,
			ocpVersion,
			expected.BC.DualPort,
		)
	}
	if features.Flags.BC.PTPHA != expected.BC.PTPHA {
		t.Errorf("linuxptp version %s, ocpVersion %s BC PTPHA should be %v",
			linuxptpVersion,
			ocpVersion,
			expected.BC.PTPHA,
		)
	}

	if features.Flags.GM.Enabled != expected.GM.Enabled {
		t.Errorf("linuxptp version %s, ocpVersion %s GM Enabled should be %v",
			linuxptpVersion,
			ocpVersion,
			expected.GM.Enabled,
		)
	}
	if features.Flags.GM.HoldOver != expected.GM.HoldOver {
		t.Errorf("linuxptp version %s, ocpVersion %s GM HoldOver should be %v",
			linuxptpVersion,
			ocpVersion,
			expected.GM.HoldOver,
		)
	}
	if features.Flags.GM.SyncE != expected.GM.SyncE {
		t.Errorf("linuxptp version %s, ocpVersion %s GM SyncE should be %v",
			linuxptpVersion,
			ocpVersion,
			expected.GM.SyncE,
		)
	}

	if features.Flags.LogSeverity != expected.LogSeverity {
		t.Errorf("linuxptp version %s, ocpVersion %s LogSeverity should be %v",
			linuxptpVersion,
			ocpVersion,
			expected.LogSeverity,
		)
	}
}
