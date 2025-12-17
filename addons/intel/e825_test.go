package intel

import (
	"fmt"
	"slices"
	"testing"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

type mockPinConfig struct {
	actualPinSetCount int
	actualPinFrqCount int
}

func (m *mockPinConfig) applyPinSet(_ string, pins pinSet) error {
	m.actualPinSetCount += len(pins)
	return nil
}

func (m *mockPinConfig) applyPinFrq(_ string, frq frqSet) error {
	m.actualPinFrqCount += len(frq)
	return nil
}

func setupMockPinConfig() (*mockPinConfig, func()) {
	mockPins := mockPinConfig{}
	origPinConfig := pinConfig
	pinConfig = &mockPins
	return &mockPins, func() { pinConfig = origPinConfig }
}

func Test_E825(t *testing.T) {
	p, d := E825("e825")
	assert.NotNil(t, p)
	assert.NotNil(t, d)

	p, d = E825("not_e825")
	assert.Nil(t, p)
	assert.Nil(t, d)
}

func Test_AfterRunPTPCommandE825(t *testing.T) {
	unitTest = true
	profile, err := loadProfile("./testdata/e825-tgm.yaml")
	assert.NoError(t, err)
	p, d := E825("e825")
	data := (*d).(*E825PluginData)

	err = p.AfterRunPTPCommand(d, profile, "bad command")
	assert.NoError(t, err)

	mockExec, execRestore := setupExecMock()
	defer execRestore()
	mockExec.setDefaults("output", nil)
	err = p.AfterRunPTPCommand(d, profile, "gpspipe")
	assert.NoError(t, err)
	// Ensure all 9 required calls are present:
	requiredUblxCmds := []string{
		"CFG-MSG,1,34,1",
		"CFG-MSG,1,3,1",
		"CFG-MSG,0xf0,0x02,0",
		"CFG-MSG,0xf0,0x03,0",
		"CFG-MSGOUT-NMEA_ID_VTG_USB,0",
		"CFG-MSGOUT-NMEA_ID_GST_USB,0",
		"CFG-MSGOUT-NMEA_ID_ZDA_USB,0",
		"CFG-MSGOUT-NMEA_ID_GBS_USB,0",
		"SAVE",
	}
	found := make([]string, 0, len(requiredUblxCmds))
	for _, call := range mockExec.actualCalls {
		for _, arg := range call.args {
			if slices.Contains(requiredUblxCmds, arg) {
				found = append(found, arg)
			}
		}
	}
	assert.Equal(t, requiredUblxCmds, found)
	// And expect 3 of them to have produced output (as specified in the profile)
	assert.Equal(t, 3, len(*data.hwplugins))
}

func Test_AfterRunPTPCommandE825_TBC(t *testing.T) {
	tcs := []struct {
		name            string
		command         string
		profile         string
		expectedPinSets int
		expectedPinFrqs int
	}{
		{
			command:         "tbc-ho-exit",
			profile:         "./testdata/e825-tbc.yaml",
			expectedPinSets: 2,
			expectedPinFrqs: 2,
		},
		{
			command:         "tbc-ho-entry",
			profile:         "./testdata/e825-tbc.yaml",
			expectedPinSets: 2,
			expectedPinFrqs: 0,
		},
		{
			command:         "tbc-ho-exit",
			profile:         "./testdata/e825-tgm.yaml",
			expectedPinSets: 0,
			expectedPinFrqs: 0,
		},
		{
			command:         "tbc-ho-entry",
			profile:         "./testdata/e825-tgm.yaml",
			expectedPinSets: 0,
			expectedPinFrqs: 0,
		},
	}
	for _, tc := range tcs {
		t.Run(fmt.Sprintf("%s::%s", tc.command, tc.profile), func(tt *testing.T) {
			mockPins, restorePins := setupMockPinConfig()
			defer restorePins()
			profile, err := loadProfile(tc.profile)
			assert.NoError(tt, err)
			p, d := E825("e825")
			err = p.AfterRunPTPCommand(d, profile, tc.command)
			assert.NoError(tt, err)
			assert.Equal(tt, tc.expectedPinSets, mockPins.actualPinSetCount)
			assert.Equal(tt, tc.expectedPinFrqs, mockPins.actualPinFrqCount)
		})
	}
}

func Test_PopulateHwConfdigE825(t *testing.T) {
	p, d := E825("e825")
	data := (*d).(*E825PluginData)
	err := p.PopulateHwConfig(d, nil)
	assert.NoError(t, err)

	output := []ptpv1.HwConfig{}
	err = p.PopulateHwConfig(d, &output)
	assert.NoError(t, err)
	assert.Equal(t, 0, len(output))

	data.hwplugins = &[]string{"A", "B", "C"}
	err = p.PopulateHwConfig(d, &output)
	assert.NoError(t, err)
	assert.Equal(t, []ptpv1.HwConfig{
		{
			DeviceID: "e825",
			Status:   "A",
		},
		{
			DeviceID: "e825",
			Status:   "B",
		},
		{
			DeviceID: "e825",
			Status:   "C",
		},
	},
		output)
}

func Test_setupGnss(t *testing.T) {
	unitTest = true
	tcs := []struct {
		name             string
		gnss             GnssOptions
		dpll             []*dpll.PinInfo
		expectError      bool
		expectedCmdCount int
	}{
		{
			name:        "No DPLL Pins",
			dpll:        []*dpll.PinInfo{},
			expectError: true,
		},
		{
			name: "No matching GNSS Pins",
			dpll: []*dpll.PinInfo{
				{
					ID:           1,
					BoardLabel:   "SkipMe",
					Type:         dpll.PinTypeEXT,
					Capabilities: dpll.PinCapState,
				},
				{
					ID:           2,
					BoardLabel:   "SkipMe-Too",
					Type:         dpll.PinTypeGNSS,
					Capabilities: 0,
				},
			},
			expectError: true,
		},
		{
			name: "Single matching pin (enable)",
			gnss: GnssOptions{
				Disabled: false,
			},
			dpll: []*dpll.PinInfo{
				{
					ID:           1,
					BoardLabel:   "SkipMe",
					Type:         dpll.PinTypeEXT,
					Capabilities: dpll.PinCapPrio,
				},
				{
					BoardLabel:   "GNSS_1PPS_IN",
					ID:           2,
					Type:         dpll.PinTypeGNSS,
					Capabilities: dpll.PinCapPrio | dpll.PinCapState,
					ParentDevice: []dpll.PinParentDevice{
						{
							ParentID:  uint32(1),
							Direction: dpll.PinDirectionInput,
						},
						{
							ParentID:  uint32(2),
							Direction: dpll.PinDirectionInput,
						},
					},
				},
			},
			expectedCmdCount: 1,
		},
		{
			name: "Single matching pin (disable)",
			gnss: GnssOptions{
				Disabled: true,
			},
			dpll: []*dpll.PinInfo{
				{
					ID:           1,
					Type:         dpll.PinTypeEXT,
					Capabilities: dpll.PinCapPrio,
				},
				{
					ID:           2,
					Type:         dpll.PinTypeGNSS,
					Capabilities: dpll.PinCapPrio | dpll.PinCapState,
					ParentDevice: []dpll.PinParentDevice{
						{
							ParentID:  uint32(1),
							Direction: dpll.PinDirectionInput,
						},
						{
							ParentID:  uint32(2),
							Direction: dpll.PinDirectionInput,
						},
					},
				},
			},
			expectedCmdCount: 1,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(tt *testing.T) {
			mockPinSet, restorePinSet := setupBatchPinSetMock()
			defer restorePinSet()
			data := E825PluginData{
				dpllPins: tc.dpll,
			}
			err := data.setupGnss(tc.gnss)
			if tc.expectError {
				assert.Error(tt, err)
			} else {
				assert.NoError(tt, err)
				assert.Equal(tt, tc.expectedCmdCount, len(*mockPinSet.commands))
				expectedState := uint32(dpll.PinStateSelectable)
				if tc.gnss.Disabled {
					expectedState = uint32(dpll.PinStateDisconnected)
				}
				for _, cmd := range *mockPinSet.commands {
					for _, ctrl := range cmd.PinParentCtl {
						assert.Equal(tt, expectedState, *ctrl.State)
					}
				}
			}
		})
	}
}

func Test_OnPTPConfigChangeE825(t *testing.T) {
	tcs := []struct {
		name            string
		profile         string
		editProfile     func(*ptpv1.PtpProfile)
		expectError     bool
		expectedPinSets int
		expectedPinFrqs int
	}{
		{
			name:    "TGM Profile",
			profile: "./testdata/e825-tgm.yaml",
		},
		{
			name:            "TBC Profile",
			profile:         "./testdata/e825-tbc.yaml",
			expectedPinSets: 2,
		},
		{
			name:    "TBC with no leadingInterface",
			profile: "./testdata/e825-tbc.yaml",
			editProfile: func(p *ptpv1.PtpProfile) {
				delete(p.PtpSettings, "leadingInterface")
			},
			expectError: true,
		},
		{
			name:    "TBC with no upstreamPort",
			profile: "./testdata/e825-tbc.yaml",
			editProfile: func(p *ptpv1.PtpProfile) {
				delete(p.PtpSettings, "upstreamPort")
			},
			expectError: true,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(tt *testing.T) {
			unitTest = true
			mockPins, restorePins := setupMockPinConfig()
			defer restorePins()
			profile, err := loadProfile(tc.profile)
			if tc.editProfile != nil {
				tc.editProfile(profile)
			}
			assert.NoError(tt, err)
			p, d := E825("e825")
			err = p.OnPTPConfigChange(d, profile)
			if tc.expectError {
				assert.Error(tt, err)
			} else {
				assert.NoError(tt, err)
				assert.Equal(tt, tc.expectedPinSets, mockPins.actualPinSetCount)
				assert.Equal(tt, tc.expectedPinFrqs, mockPins.actualPinFrqCount)
			}
		})
	}
}
