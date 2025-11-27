package intel

import (
	"slices"
	"testing"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

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

type mockBatchPinSet struct {
	commands *[]dpll.PinParentDeviceCtl
}

func (m *mockBatchPinSet) mock(commands *[]dpll.PinParentDeviceCtl) error {
	m.commands = commands
	return nil
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
			mockPinSet := mockBatchPinSet{}
			e825DoPinSet = mockPinSet.mock
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
