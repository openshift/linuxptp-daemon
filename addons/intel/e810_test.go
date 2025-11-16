package intel

import (
	"errors"
	"os"
	"slices"
	"testing"

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
	unitTest = true
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
		"CFG-MSG,1,34,1",
		"CFG-MSG,1,3,1",
		"CFG-MSG,0xf0,0x02,0",
		"CFG-MSG,0xf0,0x03,0",
		"CFG-MSGOUT-NMEA_ID_VTG_I2C,0",
		"CFG-MSGOUT-NMEA_ID_GST_I2C,0",
		"CFG-MSGOUT-NMEA_ID_ZDA_I2C,0",
		"CFG-MSGOUT-NMEA_ID_GBS_I2C,0",
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
	unitTest = true
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	profile, err := loadProfile("./testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")
	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
	assert.NotNil(t, mockPinSet.commands, "Ensure clockChain.SetPinDefaults was called")
}

// Test that the profile with no phase inputs is processed correctly
func Test_ProcessProfileTBCNoPhaseInputs(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()

	// Setup filesystem mock for TBC profile - EnableE810Outputs needs this
	mockFS, restoreFs := setupMockFS()
	defer restoreFs()

	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}

	// EnableE810Outputs reads the ptp directory and writes to SMA2 and period
	mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)

	profile, err := loadProfile("./testdata/profile-tbc-no-input-delays.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")
	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)

	// Verify that clockChain was initialized (SetPinDefaults is called as part of InitClockChain)
	// If SetPinDefaults wasn't called, InitClockChain would have failed
	assert.NotNil(t, clockChain, "clockChain should be initialized")
	assert.Equal(t, ClockTypeTBC, clockChain.Type, "clockChain should be T-BC type")
	assert.NotNil(t, mockPinSet.commands, "Ensure clockChain.SetPinDefaults was called")

	// Verify all expected filesystem calls were made
	mockFS.VerifyAllCalls(t)
}

func Test_ProcessProfileTGMOld(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	unitTest = true
	profile, err := loadProfile("./testdata/profile-tgm-old.yaml")
	assert.NoError(t, err)
	p, d := E810("e810")
	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
	assert.NotNil(t, mockPinSet.commands, "Ensure some pins were set")
}

func TestEnableE810Outputs(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(*MockFileSystem)
		clockChain    *ClockChain
		expectedError string
	}{
		{
			name: "Successful execution - single PHC",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{
					MockDirEntry{name: "ptp0", isDir: true},
				}
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)
			},
			expectedError: "",
		},
		{
			name: "ReadDir fails",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{}, errors.New("permission denied"))
			},
			expectedError: "e810 failed to read /sys/class/net/ens4f0/device/ptp/: permission denied",
		},
		{
			name: "No PHC directories found",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", []os.DirEntry{}, nil)
			},
			expectedError: "e810 cards should have one PHC per NIC, but ens4f0 has 0",
		},
		{
			name: "Multiple PHC directories found (warning case)",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{
					MockDirEntry{name: "ptp0", isDir: true},
					MockDirEntry{name: "ptp1", isDir: true},
				}
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)
			},
			expectedError: "",
		},
		{
			name: "SMA2 write fails",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{
					MockDirEntry{name: "ptp0", isDir: true},
				}
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), errors.New("write failed"))
			},
			expectedError: "e810 failed to write 2 2 to /sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2: write failed",
		},
		{
			name: "Period write fails - should not return error but log",
			clockChain: &ClockChain{
				LeadingNIC: CardInfo{Name: "ens4f0"},
			},
			setupMock: func(m *MockFileSystem) {
				phcEntries := []os.DirEntry{
					MockDirEntry{name: "ptp0", isDir: true},
				}
				m.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), errors.New("period write failed"))
			},
			expectedError: "", // Function doesn't return error for period write failure
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

func Test_PopulateHwConfdigE810(t *testing.T) {
	p, d := E810("e810")
	data := (*d).(*E810PluginData)
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
