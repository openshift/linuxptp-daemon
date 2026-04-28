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
		"CFG-HW-ANT_CFG_VOLTCTRL,1",
		"GPS",
		"Galileo",
		"GLONASS",
		"BeiDou",
		"SBAS",
		"SURVEYIN,600,50000",
		"MON-HW",
		"CFG-MSG,1,38,248",
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
	assert.Equal(t, 3, len(data.hwplugins))
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

	data.hwplugins = []string{"A", "B", "C"}
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
				assert.Equal(tt, tc.expectedCmdCount, len(mockPinSet.commands))
				expectedState := uint32(dpll.PinStateSelectable)
				if tc.gnss.Disabled {
					expectedState = uint32(dpll.PinStateDisconnected)
				}
				for _, cmd := range mockPinSet.commands {
					for _, ctrl := range cmd.PinParentCtl {
						assert.Equal(tt, expectedState, *ctrl.State)
					}
				}
			}
		})
	}
}

func makeRefPins(ref0p, ref0n bool) []*dpll.PinInfo {
	pins := []*dpll.PinInfo{}
	if ref0p {
		pins = append(pins, &dpll.PinInfo{
			ID: 0, ClockID: testClockID, PackageLabel: "REF0P", BoardLabel: "ETH01_SDP_TIMESYNC_2",
			Capabilities: dpll.PinCapState,
			ParentDevice: []dpll.PinParentDevice{
				{ParentID: 1, Direction: dpll.PinDirectionInput},
				{ParentID: 2, Direction: dpll.PinDirectionInput},
			},
		})
	}
	if ref0n {
		pins = append(pins, &dpll.PinInfo{
			ID: 1, ClockID: testClockID, PackageLabel: "REF0N", BoardLabel: "ETH01_SDP_TIMESYNC_0",
			Capabilities: dpll.PinCapState,
			ParentDevice: []dpll.PinParentDevice{
				{ParentID: 1, Direction: dpll.PinDirectionInput},
				{ParentID: 2, Direction: dpll.PinDirectionInput},
			},
		})
	}
	return pins
}

func Test_setupDpllInputPins(t *testing.T) {
	defaultDevices := testDpllDevices()
	tcs := []struct {
		name             string
		dpllPins         []*dpll.PinInfo
		dpllDevices      []*dpll.DoDeviceGetReply
		expectedCmdCount int
		expectPPSOnly    bool
	}{
		{
			name:             "Both REF0P and REF0N present",
			dpllPins:         makeRefPins(true, true),
			dpllDevices:      defaultDevices,
			expectedCmdCount: 2,
			expectPPSOnly:    true,
		},
		{
			name:             "Only REF0P present",
			dpllPins:         makeRefPins(true, false),
			dpllDevices:      defaultDevices,
			expectedCmdCount: 1,
			expectPPSOnly:    true,
		},
		{
			name: "No matching pins",
			dpllPins: []*dpll.PinInfo{
				{ID: 99, PackageLabel: "REF4P", Capabilities: dpll.PinCapState},
			},
			dpllDevices:      defaultDevices,
			expectedCmdCount: 0,
		},
		{
			name: "Pin lacks PinCapState",
			dpllPins: []*dpll.PinInfo{
				{
					ID: 1, ClockID: testClockID, PackageLabel: "REF0P",
					Capabilities: dpll.PinCapPrio,
					ParentDevice: []dpll.PinParentDevice{
						{ParentID: 1, Direction: dpll.PinDirectionInput},
						{ParentID: 2, Direction: dpll.PinDirectionInput},
					},
				},
			},
			dpllDevices:      defaultDevices,
			expectedCmdCount: 0,
		},
		{
			name: "PPS parent is output direction",
			dpllPins: []*dpll.PinInfo{
				{
					ID: 1, ClockID: testClockID, PackageLabel: "REF0P",
					Capabilities: dpll.PinCapState,
					ParentDevice: []dpll.PinParentDevice{
						{ParentID: 1, Direction: dpll.PinDirectionInput},
						{ParentID: 2, Direction: dpll.PinDirectionOutput},
					},
				},
			},
			dpllDevices:      defaultDevices,
			expectedCmdCount: 0,
		},
		{
			name: "No PPS device in devices list",
			dpllPins: []*dpll.PinInfo{
				{
					ID: 1, ClockID: testClockID, PackageLabel: "REF0P",
					Capabilities: dpll.PinCapState,
					ParentDevice: []dpll.PinParentDevice{
						{ParentID: 1, Direction: dpll.PinDirectionInput},
						{ParentID: 2, Direction: dpll.PinDirectionInput},
					},
				},
			},
			dpllDevices: []*dpll.DoDeviceGetReply{
				{ID: 1, ClockID: testClockID, Type: dpll.DpllTypeEEC},
				{ID: 2, ClockID: testClockID, Type: dpll.DpllTypeEEC},
			},
			expectedCmdCount: 0,
		},
		{
			name: "ClockID mismatch between pin and device",
			dpllPins: []*dpll.PinInfo{
				{
					ID: 1, ClockID: 9999, PackageLabel: "REF0P",
					Capabilities: dpll.PinCapState,
					ParentDevice: []dpll.PinParentDevice{
						{ParentID: 1, Direction: dpll.PinDirectionInput},
						{ParentID: 2, Direction: dpll.PinDirectionInput},
					},
				},
			},
			dpllDevices:      defaultDevices,
			expectedCmdCount: 0,
		},
		{
			name: "Pin has only EEC parent, no PPS parent",
			dpllPins: []*dpll.PinInfo{
				{
					ID: 1, ClockID: testClockID, PackageLabel: "REF0P",
					Capabilities: dpll.PinCapState,
					ParentDevice: []dpll.PinParentDevice{
						{ParentID: 1, Direction: dpll.PinDirectionInput},
					},
				},
			},
			dpllDevices:      defaultDevices,
			expectedCmdCount: 0,
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(tt *testing.T) {
			mockPinSet, restorePinSet := setupBatchPinSetMock()
			defer restorePinSet()
			data := E825PluginData{dpllPins: tc.dpllPins, dpllDevices: tc.dpllDevices}
			err := data.setupDpllInputPins()
			assert.NoError(tt, err)
			assert.Equal(tt, tc.expectedCmdCount, len(mockPinSet.commands))
			if tc.expectPPSOnly {
				for _, cmd := range mockPinSet.commands {
					assert.Len(tt, cmd.PinParentCtl, 1, "must target PPS parent only")
					assert.Equal(tt, uint32(dpll.PinStateSelectable), *cmd.PinParentCtl[0].State)
				}
			}
		})
	}
}

func Test_OnPTPConfigChangeE825(t *testing.T) {
	tcs := []struct {
		name             string
		profile          string
		editProfile      func(*ptpv1.PtpProfile)
		expectError      bool
		expectedPinSets  int
		expectedPinFrqs  int
		expectedDpllCmds int
	}{
		{
			name:             "TGM Profile",
			profile:          "./testdata/e825-tgm.yaml",
			expectedDpllCmds: 1,
		},
		{
			name:             "TBC Profile",
			profile:          "./testdata/e825-tbc.yaml",
			expectedPinSets:  2,
			expectedDpllCmds: 3,
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
			mockPins, restorePins := setupMockPinConfig()
			defer restorePins()
			profile, err := loadProfile(tc.profile)
			if tc.editProfile != nil {
				tc.editProfile(profile)
			}
			assert.NoError(tt, err)
			p, d := E825("e825")
			data := (*d).(*E825PluginData)
			mockDpllPinset, restoreDpllPins := setupGNSSMocks(data)
			defer restoreDpllPins()
			err = p.OnPTPConfigChange(d, profile)
			if tc.expectError {
				assert.Error(tt, err)
			} else {
				assert.NoError(tt, err)
				assert.Equal(tt, tc.expectedPinSets, mockPins.actualPinSetCount)
				assert.Equal(tt, tc.expectedPinFrqs, mockPins.actualPinFrqCount)
				assert.Equal(tt, tc.expectedDpllCmds, len(mockDpllPinset.commands))
			}
		})
	}
}
