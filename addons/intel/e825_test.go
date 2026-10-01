package intel

import (
	"encoding/json"
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

func assertPinParentState(t *testing.T, commands []dpll.PinParentDeviceCtl, pinID, parentID, expected uint32) {
	t.Helper()
	for _, command := range commands {
		if command.ID != pinID {
			continue
		}
		for _, control := range command.PinParentCtl {
			if control.PinParentID == parentID && control.State != nil {
				assert.Equal(t, expected, *control.State)
				return
			}
		}
	}
	assert.Fail(t, fmt.Sprintf("no state command for pin %d parent %d", pinID, parentID))
}

func assertPinParentNotConfigured(t *testing.T, commands []dpll.PinParentDeviceCtl, pinID, parentID uint32) {
	t.Helper()
	for _, command := range commands {
		if command.ID != pinID {
			continue
		}
		for _, control := range command.PinParentCtl {
			assert.NotEqual(t, parentID, control.PinParentID)
		}
	}
}

func Test_populateDpllDevicesRefreshesInventory(t *testing.T) {
	originalGetAllDpllDevices := getAllDpllDevices
	defer func() { getAllDpllDevices = originalGetAllDpllDevices }()

	calls := 0
	getAllDpllDevices = func() ([]*dpll.DoDeviceGetReply, error) {
		calls++
		return testDpllDevices(), nil
	}

	data := E825PluginData{}
	assert.NoError(t, data.populateDpllDevices())
	assert.NoError(t, data.populateDpllDevices())
	assert.Equal(t, 2, calls)
	assert.Equal(t, testDpllDevices(), data.dpllDevices)
}

func Test_populateDpllDevicesRejectsNilInventory(t *testing.T) {
	originalGetAllDpllDevices := getAllDpllDevices
	defer func() { getAllDpllDevices = originalGetAllDpllDevices }()
	getAllDpllDevices = func() ([]*dpll.DoDeviceGetReply, error) { return nil, nil }

	data := E825PluginData{}
	err := data.populateDpllDevices()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DPLL device dump returned a nil inventory")
	assert.Nil(t, data.dpllDevices)
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

func Test_OnPTPConfigChangeE825(t *testing.T) {
	tcs := []struct {
		name             string
		profile          string
		editProfile      func(*ptpv1.PtpProfile)
		disableGNSS      bool
		expectError      bool
		expectedPinSets  int
		expectedPinFrqs  int
		expectedDpllCmds int
	}{
		{
			name:             "TGM Profile",
			profile:          "./testdata/e825-tgm.yaml",
			expectedPinSets:  2,
			expectedDpllCmds: 1,
		},
		{
			name:             "TGM Profile with GNSS disabled",
			profile:          "./testdata/e825-tgm.yaml",
			disableGNSS:      true,
			expectedPinSets:  2,
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
			if tc.disableGNSS {
				var e825Opts map[string]interface{}
				assert.NoError(tt, json.Unmarshal(profile.Plugins[pluginNameE825].Raw, &e825Opts))
				e825Opts["gnss"] = map[string]interface{}{"disabled": true}
				pluginOpts, marshalErr := json.Marshal(e825Opts)
				assert.NoError(tt, marshalErr)
				profile.Plugins[pluginNameE825].Raw = pluginOpts
			}
			p, d := E825("e825")
			data := (*d).(*E825PluginData)
			mockDpllPinset, restoreDpllPins := setupGNSSMocks(data)
			defer restoreDpllPins()
			devices := getAllDpllDevices
			deviceDumps := 0
			getAllDpllDevices = func() ([]*dpll.DoDeviceGetReply, error) {
				deviceDumps++
				return devices()
			}
			defer func() { getAllDpllDevices = devices }()
			pins := getAllDpllPins
			pinDumps := 0
			getAllDpllPins = func() ([]*dpll.PinInfo, error) {
				pinDumps++
				return pins()
			}
			defer func() { getAllDpllPins = pins }()
			err = p.OnPTPConfigChange(d, profile)
			assert.Equal(tt, 1, deviceDumps)
			assert.Equal(tt, 1, pinDumps)
			if tc.expectError {
				assert.Error(tt, err)
			} else {
				assert.NoError(tt, err)
				assert.Equal(tt, tc.expectedPinSets, mockPins.actualPinSetCount)
				assert.Equal(tt, tc.expectedPinFrqs, mockPins.actualPinFrqCount)
				assert.Equal(tt, tc.expectedDpllCmds, len(mockDpllPinset.commands))
				if tc.name == "TBC Profile" {
					for _, pinID := range []uint32{10, 11} {
						assertPinParentState(tt, mockDpllPinset.commands, pinID, 1, uint32(dpll.PinStateDisconnected))
						assertPinParentState(tt, mockDpllPinset.commands, pinID, 2, uint32(dpll.PinStateSelectable))
						assertPinParentNotConfigured(tt, mockDpllPinset.commands, pinID, 3)
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
				{ParentID: 2, Direction: dpll.PinDirectionInput},
				{ParentID: 3, Direction: dpll.PinDirectionInput},
				{ParentID: 1, Direction: dpll.PinDirectionInput},
			},
		})
	}
	if ref0n {
		pins = append(pins, &dpll.PinInfo{
			ID: 1, ClockID: testClockID, PackageLabel: "REF0N", BoardLabel: "ETH01_SDP_TIMESYNC_0",
			Capabilities: dpll.PinCapState,
			ParentDevice: []dpll.PinParentDevice{
				{ParentID: 2, Direction: dpll.PinDirectionInput},
				{ParentID: 3, Direction: dpll.PinDirectionInput},
				{ParentID: 1, Direction: dpll.PinDirectionInput},
			},
		})
	}
	return pins
}

func Test_setupDpllInputPins(t *testing.T) {
	defaultDevices := testDpllDevices()
	tcs := []struct {
		name              string
		dpllPins          []*dpll.PinInfo
		dpllDevices       []*dpll.DoDeviceGetReply
		expectedCmdCount  int
		expectBothParents bool
	}{
		{
			name:              "Both REF0P and REF0N present",
			dpllPins:          makeRefPins(true, true),
			dpllDevices:       defaultDevices,
			expectedCmdCount:  2,
			expectBothParents: true,
		},
		{
			name:              "Only REF0P present",
			dpllPins:          makeRefPins(true, false),
			dpllDevices:       defaultDevices,
			expectedCmdCount:  1,
			expectBothParents: true,
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
			name:             "No EEC device in devices list",
			dpllPins:         makeRefPins(true, false),
			dpllDevices:      []*dpll.DoDeviceGetReply{{ID: 2, ClockID: testClockID, Type: dpll.DpllTypePPS}},
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
			if tc.expectBothParents {
				for _, cmd := range mockPinSet.commands {
					assert.Len(tt, cmd.PinParentCtl, 2)
					states := make(map[uint32]uint32, len(cmd.PinParentCtl))
					for _, control := range cmd.PinParentCtl {
						states[control.PinParentID] = *control.State
					}
					assert.Equal(tt, uint32(dpll.PinStateDisconnected), states[1], "EEC parent")
					assert.Equal(tt, uint32(dpll.PinStateSelectable), states[2], "PPS parent")
					assert.NotContains(tt, states, uint32(3), "unrelated parent")
				}
			}
		})
	}
}

func Test_OnPTPConfigChangeE825FailsBeforeConfigurationWhenDpllInventoryFails(t *testing.T) {
	mockPins, restorePins := setupMockPinConfig()
	defer restorePins()
	profile, err := loadProfile("./testdata/e825-tbc.yaml")
	assert.NoError(t, err)
	p, d := E825("e825")
	data := (*d).(*E825PluginData)
	mockDpllPinset, restoreDpllPins := setupGNSSMocks(data)
	defer restoreDpllPins()
	data.dpllDevices = nil

	originalGetAllDpllDevices := getAllDpllDevices
	defer func() { getAllDpllDevices = originalGetAllDpllDevices }()
	deviceDumps := 0
	getAllDpllDevices = func() ([]*dpll.DoDeviceGetReply, error) {
		deviceDumps++
		return nil, fmt.Errorf("device dump failed")
	}
	pinDumps := 0
	getAllDpllPins = func() ([]*dpll.PinInfo, error) {
		pinDumps++
		return data.dpllPins, nil
	}

	err = p.OnPTPConfigChange(d, profile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to initialize E825 DPLL device inventory")
	assert.Equal(t, 1, deviceDumps)
	assert.Zero(t, pinDumps, "pin inventory should not be queried after device initialization fails")
	assert.Zero(t, mockPins.actualPinSetCount)
	assert.Empty(t, mockDpllPinset.commands)
}
