package intel

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

func Test_E810(t *testing.T) {
	p, d := E810("e810")
	assert.NotNil(t, p)
	assert.NotNil(t, d)

	p, d = E810("not_e810")
	assert.Nil(t, p)
	assert.Nil(t, d)
}

func Test_AfterRunPTPCommandE810(t *testing.T) {
	profile, err := loadProfile("./testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")
	data := (*d).(*E810PluginData)

	err = p.AfterRunPTPCommand(d, profile, "bad command")
	assert.NoError(t, err)

	mockExec, execRestore := setupExecMock()
	defer execRestore()
	mockExec.setDefaults("output", nil)
	err = p.AfterRunPTPCommand(d, profile, "gpspipe")
	assert.NoError(t, err)
	// Ensure all 9 required calls are the last 9:
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

func Test_initInternalDelays(t *testing.T) {
	delays, err := InitInternalDelays("E810-XXVDA4T")
	assert.NoError(t, err)
	assert.Equal(t, "E810-XXVDA4T", delays.PartType)
	assert.Len(t, delays.ExternalInputs, 3)
	assert.Len(t, delays.ExternalOutputs, 3)
}

func Test_initInternalDelays_BadPart(t *testing.T) {
	_, err := InitInternalDelays("Dummy")
	assert.Error(t, err)
}

func Test_ProcessProfileTGMNew(t *testing.T) {
	_, restorePins := setupMockDPLLPinsFromJSON("./testdata/dpll-pins.json")
	defer restorePins()
	restoreDelay := setupMockDelayCompensation()
	defer restoreDelay()
	restoreHasSMA := setupMockHasSysfsSMAPins(true)
	defer restoreHasSMA()
	restoreDiscovery := setupMockPinDiscovery([]string{"SMA1", "SMA2", "U.FL1", "U.FL2"})
	defer restoreDiscovery()
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	profile, err := loadProfile("./testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")

	mockFs, restoreFs := setupMockFS()
	defer restoreFs()
	mockClockIDsFromProfile(mockFs, profile)

	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
	assert.NotNil(t, mockPinSet.commands, "Ensure clockChain.SetPinDefaults was called")
}

// Test that the profile with no phase inputs is processed correctly
func Test_ProcessProfileTBCNoPhaseInputs(t *testing.T) {
	_, restoreDPLLPins := setupMockDPLLPinsFromJSON("./testdata/dpll-pins.json")
	defer restoreDPLLPins()
	restoreDelay := setupMockDelayCompensation()
	defer restoreDelay()
	restoreHasSMA := setupMockHasSysfsSMAPins(true)
	defer restoreHasSMA()
	restoreDiscovery := setupMockPinDiscovery([]string{"SMA1", "SMA2", "U.FL1", "U.FL2"})
	defer restoreDiscovery()
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()

	// Setup filesystem mock for TBC profile - EnableE810Outputs needs this
	mockFS, restoreFs := setupMockFS()
	defer restoreFs()

	// mockPins
	mockPinConfig, restorePins := setupMockPinConfig()
	defer restorePins()

	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}

	// EnableE810Outputs reads the ptp directory and writes period (SMA2 is now via DPLL)
	mockFS.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
	mockFS.AllowReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", nil, os.ErrNotExist)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)

	profile, err := loadProfile("./testdata/profile-tbc-no-input-delays.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")

	mockClockIDsFromProfile(mockFS, profile)

	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
	assert.Equal(t, 1, mockPinConfig.actualPinSetCount, "SDP22 sysfs channel assignment for 1PPS")
	assert.Equal(t, 0, mockPinConfig.actualPinFrqCount)

	// Verify that clockChain was initialized (SetPinDefaults is called as part of InitClockChain)
	// If SetPinDefaults wasn't called, InitClockChain would have failed
	assert.NotNil(t, clockChain, "clockChain should be initialized")
	ccData := clockChain.(*ClockChain)
	assert.Equal(t, ClockTypeTBC, ccData.Type, "clockChain should be T-BC type")
	assert.NotNil(t, mockPinSet.commands, "Ensure clockChain.SetPinDefaults was called")

	// Verify all expected filesystem calls were made
	mockFS.VerifyAllCalls(t)
}

func Test_ProcessProfileTGMOld(t *testing.T) {
	_, restorePins := setupMockDPLLPinsFromJSON("./testdata/dpll-pins.json")
	defer restorePins()
	restoreDelay := setupMockDelayCompensation()
	defer restoreDelay()
	restoreHasSMA := setupMockHasSysfsSMAPins(true)
	defer restoreHasSMA()
	restoreDiscovery := setupMockPinDiscovery([]string{"SMA1", "SMA2", "U.FL1", "U.FL2"})
	defer restoreDiscovery()
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()

	// Reset clockChain so SetPinDefaults (called via the no-PhaseInputs path) uses mock DpllPins
	oldClockChain := clockChain
	clockChain = &ClockChain{DpllPins: DpllPins}
	defer func() { clockChain = oldClockChain }()

	profile, err := loadProfile("./testdata/profile-tgm-old.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")

	mockFS, restoreFs := setupMockFS()
	defer restoreFs()
	mockClockIDsFromProfile(mockFS, profile)

	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
	assert.NotNil(t, mockPinSet.commands, "Ensure some pins were set")
}

func TestEnableE810Outputs(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()

	sma2Pin := dpll.PinInfo{
		ID:           10,
		ClockID:      1000,
		BoardLabel:   "SMA2",
		Type:         dpll.PinTypeEXT,
		Capabilities: dpll.PinCapDir | dpll.PinCapPrio | dpll.PinCapState,
		ParentDevice: []dpll.PinParentDevice{
			{ParentID: 1, Direction: dpll.PinDirectionOutput},
			{ParentID: 2, Direction: dpll.PinDirectionOutput},
		},
	}

	tests := []struct {
		name          string
		setupMock     func(*MockFileSystem)
		clockChain    *ClockChain
		expectedError string
	}{
		{
			name: "DPLL path - no sysfs SMA pins",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0", DpllClockID: 1000},
				DpllPins:   &mockedDPLLPins{pins: dpllPins{&sma2Pin}},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", nil, os.ErrNotExist)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)
			},
			expectedError: "",
		},
		{
			name: "Sysfs path - SMA pins available",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0", DpllClockID: 1000},
				DpllPins:   &mockedDPLLPins{pins: dpllPins{&sma2Pin}},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", []byte("0 1"), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte{}, os.FileMode(0o666), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)
			},
			expectedError: "",
		},
		{
			name: "Sysfs path - SMA2 write fails",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0", DpllClockID: 1000},
				DpllPins:   &mockedDPLLPins{pins: dpllPins{&sma2Pin}},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", []byte("0 1"), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte{}, os.FileMode(0o666), errors.New("SMA2 write failed"))
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)
			},
			expectedError: "",
		},
		{
			name: "ReadDir fails",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0", DpllClockID: 1000},
				DpllPins:   &mockedDPLLPins{pins: dpllPins{&sma2Pin}},
			},
			setupMock: func(m *MockFileSystem) {
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{}, errors.New("permission denied"))
			},
			expectedError: "e810 failed to read /sys/class/net/ens4f0/device/ptp/: permission denied",
		},
		{
			name: "No PHC directories found",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0", DpllClockID: 1000},
				DpllPins:   &mockedDPLLPins{pins: dpllPins{&sma2Pin}},
			},
			setupMock: func(m *MockFileSystem) {
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{}, nil)
			},
			expectedError: "e810 cards should have one PHC per NIC, but ens4f0 has 0",
		},
		{
			name: "Multiple PHC directories (warning case)",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0", DpllClockID: 1000},
				DpllPins:   &mockedDPLLPins{pins: dpllPins{&sma2Pin}},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{
					MockDirEntry{name: "ptp0", isDir: true},
					MockDirEntry{name: "ptp1", isDir: true},
				}
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", nil, os.ErrNotExist)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)
			},
			expectedError: "",
		},
		{
			name: "Period write fails - should not return error but log",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0", DpllClockID: 1000},
				DpllPins:   &mockedDPLLPins{pins: dpllPins{&sma2Pin}},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", nil, os.ErrNotExist)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), errors.New("period write failed"))
			},
			expectedError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockPinSet.reset()
			DpllPins = &mockedDPLLPins{pins: dpllPins{&sma2Pin}}

			// Setup mock filesystem
			mockFS, restoreFs := setupMockFS()
			defer restoreFs()
			tt.setupMock(mockFS)

			// Execute function
			err := tt.clockChain.EnableE810Outputs()

			// Check error
			if tt.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedError)
			} else {
				assert.NoError(t, err)
			}

			// Verify all expected calls were made
			mockFS.VerifyAllCalls(t)
		})
	}
}

func Test_AfterRunPTPCommandE810ClockChain(t *testing.T) {
	profile, err := loadProfile("./testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")

	err = p.AfterRunPTPCommand(d, profile, "bad command")
	assert.NoError(t, err)

	mClockChain := &mockClockChain{}
	clockChain = mClockChain
	err = p.AfterRunPTPCommand(d, profile, "reset-to-default")
	assert.NoError(t, err)
	mClockChain.assertCallCounts(t, 0, 0, 1)

	mClockChain.returnErr = fmt.Errorf("Fake error")
	err = p.AfterRunPTPCommand(d, profile, "reset-to-default")
	assert.Error(t, err)
	mClockChain.assertCallCounts(t, 0, 0, 2)

	mClockChain = &mockClockChain{}
	clockChain = mClockChain
	err = p.AfterRunPTPCommand(d, profile, "tbc-ho-entry")
	assert.NoError(t, err)
	mClockChain.assertCallCounts(t, 0, 1, 0)
	mClockChain.returnErr = fmt.Errorf("Fake error")
	err = p.AfterRunPTPCommand(d, profile, "tbc-ho-entry")
	assert.Error(t, err)
	mClockChain.assertCallCounts(t, 0, 2, 0)

	mClockChain = &mockClockChain{}
	clockChain = mClockChain
	err = p.AfterRunPTPCommand(d, profile, "tbc-ho-exit")
	assert.NoError(t, err)
	mClockChain.assertCallCounts(t, 1, 0, 0)
	mClockChain.returnErr = fmt.Errorf("Fake error")
	err = p.AfterRunPTPCommand(d, profile, "tbc-ho-exit")
	assert.Error(t, err)
	mClockChain.assertCallCounts(t, 2, 0, 0)
}

func TestPinSetHasSMAInput(t *testing.T) {
	tests := []struct {
		name     string
		pins     pinSet
		expected bool
	}{
		{
			name:     "SMA1 input",
			pins:     pinSet{"SMA1": "1 1"},
			expected: true,
		},
		{
			name:     "SMA2 input",
			pins:     pinSet{"SMA2": "1 2"},
			expected: true,
		},
		{
			name:     "SMA1 input with leading spaces",
			pins:     pinSet{"SMA1": "  1 1  "},
			expected: true,
		},
		{
			name:     "SMA1 disabled",
			pins:     pinSet{"SMA1": "0 1"},
			expected: false,
		},
		{
			name:     "SMA2 output",
			pins:     pinSet{"SMA2": "2 2"},
			expected: false,
		},
		{
			name:     "no SMA pins",
			pins:     pinSet{"U.FL1": "1 1"},
			expected: false,
		},
		{
			name:     "empty pinset",
			pins:     pinSet{},
			expected: false,
		},
		{
			name:     "both SMA disabled",
			pins:     pinSet{"SMA1": "0 1", "SMA2": "0 2"},
			expected: false,
		},
		{
			name:     "SMA1 disabled but SMA2 input",
			pins:     pinSet{"SMA1": "0 1", "SMA2": "1 2"},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, pinSetHasSMAInput(tt.pins))
		})
	}
}

func TestDevicePins_DPLL_SMAInput_SetsGNSSPriority(t *testing.T) {
	sma1Pin := makeTwoParentPin(1, "SMA1", 1000,
		dpll.PinDirectionInput, dpll.PinDirectionInput)
	gnssPin := makeTwoParentPin(2, "GNSS-1PPS", 1000,
		dpll.PinDirectionInput, dpll.PinDirectionInput)
	pins := makePins(sma1Pin, gnssPin)

	ps := pinSet{"SMA1": "1 1"}
	commands := pins.GetCommandsForPluginPinSet(1000, ps)

	if pinSetHasSMAInput(ps) {
		gnssPinInfo := pins.GetByLabel("GNSS-1PPS", 1000)
		if gnssPinInfo != nil {
			gnssCommands := SetPinControlData(*gnssPinInfo, PinParentControl{
				EecPriority: 4,
				PpsPriority: 4,
			})
			commands = append(commands, gnssCommands...)
		}
	}

	gnssFound := false
	for _, cmd := range commands {
		if cmd.ID == 2 {
			gnssFound = true
			assert.Len(t, cmd.PinParentCtl, 2)
			for _, pc := range cmd.PinParentCtl {
				assert.NotNil(t, pc.Prio)
				assert.Equal(t, uint32(4), *pc.Prio)
			}
		}
	}
	assert.True(t, gnssFound, "GNSS-1PPS command should be present")
}

func TestDevicePins_DPLL_NoSMAInput_NoGNSSCommand(t *testing.T) {
	sma2Pin := makeTwoParentPin(1, "SMA2", 1000,
		dpll.PinDirectionOutput, dpll.PinDirectionOutput)
	gnssPin := makeTwoParentPin(2, "GNSS-1PPS", 1000,
		dpll.PinDirectionInput, dpll.PinDirectionInput)
	pins := makePins(sma2Pin, gnssPin)

	ps := pinSet{"SMA2": "2 2"}
	commands := pins.GetCommandsForPluginPinSet(1000, ps)

	if pinSetHasSMAInput(ps) {
		gnssPinInfo := pins.GetByLabel("GNSS-1PPS", 1000)
		if gnssPinInfo != nil {
			gnssCommands := SetPinControlData(*gnssPinInfo, PinParentControl{
				EecPriority: 4,
				PpsPriority: 4,
			})
			commands = append(commands, gnssCommands...)
		}
	}

	for _, cmd := range commands {
		assert.NotEqual(t, uint32(2), cmd.ID,
			"GNSS-1PPS command should NOT be present when SMA is output")
	}
}

func Test_checkPinIndex(t *testing.T) {
	strPtr := func(s string) *string { return &s }

	tests := []struct {
		name         string
		ts2phcConf   *string
		setupMock    func(*MockFileSystem)
		expectedConf *string
	}{
		{
			name:         "nil Ts2PhcConf is a no-op",
			ts2phcConf:   nil,
			expectedConf: nil,
		},
		{
			name:       "interface section without pin_index and no SMA pins gets pin_index added",
			ts2phcConf: strPtr("[global]\nts2phc.nmea_serialport /dev/gnss0\n[ens4f0]\nts2phc.extts_polarity rising"),
			setupMock: func(m *MockFileSystem) {
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}, nil)
				m.AllowReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", nil, os.ErrNotExist)
			},
			expectedConf: strPtr("[global]\nts2phc.nmea_serialport /dev/gnss0\n[ens4f0]\nts2phc.extts_polarity rising\nts2phc.pin_index 1"),
		},
		{
			name:       "interface section with existing pin_index is not duplicated",
			ts2phcConf: strPtr("[global]\n[ens4f0]\nts2phc.pin_index 0\nts2phc.extts_polarity rising"),
			setupMock: func(m *MockFileSystem) {
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}, nil)
				m.AllowReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", nil, os.ErrNotExist)
			},
			expectedConf: strPtr("[global]\n[ens4f0]\nts2phc.pin_index 0\nts2phc.extts_polarity rising"),
		},
		{
			name:         "global section does not get pin_index",
			ts2phcConf:   strPtr("[global]\nts2phc.nmea_serialport /dev/gnss0"),
			expectedConf: strPtr("[global]\nts2phc.nmea_serialport /dev/gnss0"),
		},
		{
			name:         "nmea section does not get pin_index",
			ts2phcConf:   strPtr("[nmea]\nts2phc.master 1"),
			expectedConf: strPtr("[nmea]\nts2phc.master 1"),
		},
		{
			name:       "interface with SMA pins does not get pin_index",
			ts2phcConf: strPtr("[ens4f0]\nts2phc.extts_polarity rising"),
			setupMock: func(m *MockFileSystem) {
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}, nil)
				m.AllowReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", []byte("0 1"), nil)
			},
			expectedConf: strPtr("[ens4f0]\nts2phc.extts_polarity rising"),
		},
		{
			name:       "multiple interfaces - pin_index added only where needed",
			ts2phcConf: strPtr("[global]\nts2phc.nmea_serialport /dev/gnss0\n[ens4f0]\nts2phc.extts_polarity rising\n[ens4f1]\nts2phc.pin_index 0\nts2phc.extts_polarity rising"),
			setupMock: func(m *MockFileSystem) {
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}, nil)
				m.AllowReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", nil, os.ErrNotExist)
				m.AllowReadDir("/sys/class/net/ens4f1/device/ptp/", []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}, nil)
				m.AllowReadFile("/sys/class/net/ens4f1/device/ptp/ptp0/pins/SMA1", nil, os.ErrNotExist)
			},
			expectedConf: strPtr("[global]\nts2phc.nmea_serialport /dev/gnss0\n[ens4f0]\nts2phc.extts_polarity rising\nts2phc.pin_index 1\n[ens4f1]\nts2phc.pin_index 0\nts2phc.extts_polarity rising"),
		},
		{
			name:         "empty config string is unchanged",
			ts2phcConf:   strPtr(""),
			expectedConf: strPtr(""),
		},
		{
			name:       "pin_index added to last section when at end of file",
			ts2phcConf: strPtr("[global]\n[ens4f0]\nts2phc.extts_polarity rising"),
			setupMock: func(m *MockFileSystem) {
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}, nil)
				m.AllowReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", nil, os.ErrNotExist)
			},
			expectedConf: strPtr("[global]\n[ens4f0]\nts2phc.extts_polarity rising\nts2phc.pin_index 1"),
		},
		{
			name:       "hasSysfsSMAPins returns false when ReadDir fails",
			ts2phcConf: strPtr("[ens4f0]\nts2phc.extts_polarity rising"),
			setupMock: func(m *MockFileSystem) {
				m.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", nil, fmt.Errorf("no such directory"))
			},
			expectedConf: strPtr("[ens4f0]\nts2phc.extts_polarity rising\nts2phc.pin_index 1"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockFS, restoreFs := setupMockFS()
			defer restoreFs()
			if tt.setupMock != nil {
				tt.setupMock(mockFS)
			}

			profile := &ptpv1.PtpProfile{
				Ts2PhcConf: tt.ts2phcConf,
			}

			checkPinIndex(profile)

			if tt.expectedConf == nil {
				assert.Nil(t, profile.Ts2PhcConf)
			} else {
				assert.NotNil(t, profile.Ts2PhcConf)
				assert.Equal(t, *tt.expectedConf, *profile.Ts2PhcConf)
			}
		})
	}
}

func Test_PopulateHwConfdigE810(t *testing.T) {
	p, d := E810("e810")
	data := (*d).(*E810PluginData)
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
			DeviceID: "e810",
			Status:   "A",
		},
		{
			DeviceID: "e810",
			Status:   "B",
		},
		{
			DeviceID: "e810",
			Status:   "C",
		},
	},
		output)
}
