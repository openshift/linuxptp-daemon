package features_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/features"
)

func TestOCPFlags(t *testing.T) {
	cases := []struct {
		versionStrs   []string
		expectedFlags features.Features
	}{
		{
			[]string{"4.12"},
			features.Features{
				OC: features.OCFeatures{
					Enabled: true,
				},
				BC: features.BCFeatures{
					Enabled: true,
				},
			},
		},
		{
			[]string{"4.13"},
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
			[]string{"4.15", "4.14"},
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
			[]string{"4.18", "4.17", "4.16"},
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
			[]string{"4.20", "4.19"},
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
		for _, ocpVersion := range c.versionStrs {
			clearFeatures()
			checkFlags(t, features.VersionLinuxPTP441.String(), ocpVersion, c.expectedFlags)
		}
	}
}
