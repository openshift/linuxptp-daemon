package intel

import (
	"fmt"
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

func Test_E830(t *testing.T) {
	p, d := E830("e830")
	assert.NotNil(t, p)
	assert.NotNil(t, d)

	p, d = E830("not_e830")
	assert.Nil(t, p)
	assert.Nil(t, d)
}

func Test_OnPTPConfigChangeE830(t *testing.T) {
	tcs := []struct {
		name                string
		profile             string
		foundDpll           bool
		editProfile         func(*ptpv1.PtpProfile)
		expectError         bool
		expectedPinSets     int
		expectedPinFrqs     int
		expectedPtpSettings map[string]string
	}{
		{
			name:      "TGM Profile",
			profile:   "./testdata/e825-tgm.yaml",
			foundDpll: true,
			expectedPtpSettings: map[string]string{
				"dpll.enp108s0f0.ignore": "",
				"clockId[enp108s0f0]":    "0",
				"dpll.enp108s0f0.flags":  "5",
			},
		},
		{
			name:      "TBC Profile",
			profile:   "./testdata/e825-tbc.yaml",
			foundDpll: true,
			expectedPtpSettings: map[string]string{
				"dpll.enp108s0f0.ignore": "",
				"clockId[enp108s0f0]":    "0",
				"dpll.enp108s0f0.flags":  "5",
			},
		},
		{
			name:      "TGM Profile (No DPLL)",
			profile:   "./testdata/e825-tgm.yaml",
			foundDpll: false,
			expectedPtpSettings: map[string]string{
				"dpll.enp108s0f0.ignore": "true",
				"clockId[enp108s0f0]":    "0",
				"dpll.enp108s0f0.flags":  "",
			},
		},
		{
			name:      "TBC Profile (No DPLL)",
			profile:   "./testdata/e825-tbc.yaml",
			foundDpll: false,
			expectedPtpSettings: map[string]string{
				"dpll.enp108s0f0.ignore": "true",
				"clockId[enp108s0f0]":    "0",
				"dpll.enp108s0f0.flags":  "",
			},
		},
		{
			name:      "TBC with no leadingInterface",
			profile:   "./testdata/e825-tbc.yaml",
			foundDpll: true,
			editProfile: func(p *ptpv1.PtpProfile) {
				delete(p.PtpSettings, "leadingInterface")
			},
			expectError: true,
		},
		{
			name:      "TBC with no upstreamPort",
			profile:   "./testdata/e825-tbc.yaml",
			foundDpll: true,
			editProfile: func(p *ptpv1.PtpProfile) {
				delete(p.PtpSettings, "upstreamPort")
			},
			expectError: true,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(tt *testing.T) {
			// Mock pin setup
			mockPins, restorePins := setupMockPinConfig()
			defer restorePins()

			// Mock DPLL detection
			hasDpllForClockID = func(_ uint64) bool {
				return tc.foundDpll
			}
			defer func() { hasDpllForClockID = _hasDpllForClockID }()

			profile, err := loadProfile(tc.profile)
			if tc.editProfile != nil {
				tc.editProfile(profile)
			}
			assert.NoError(tt, err)
			p, d := E830("e830")
			err = p.OnPTPConfigChange(d, profile)
			if tc.expectError {
				assert.Error(tt, err)
			} else {
				assert.NoError(tt, err)
				assert.Equal(tt, tc.expectedPinSets, mockPins.actualPinSetCount)
				assert.Equal(tt, tc.expectedPinFrqs, mockPins.actualPinFrqCount)
				for key, value := range tc.expectedPtpSettings {
					assert.Equal(tt, value, profile.PtpSettings[key], fmt.Sprintf("PtpSettings[%s]", key))
				}
			}
		})
	}
}
