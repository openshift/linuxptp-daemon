package intel

import (
	"errors"
	"os"
	"testing"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/yaml"
)

type mockBatchPinSet struct {
	commands *[]dpll.PinParentDeviceCtl
}

func (m *mockBatchPinSet) mock(commands *[]dpll.PinParentDeviceCtl) error {
	m.commands = commands
	return nil
}

func (m *mockBatchPinSet) reset() {
	m.commands = nil
}

func setupBatchPinSetMock() (*mockBatchPinSet, func()) {
	originalBatchPinset := BatchPinSet
	mock := &mockBatchPinSet{}
	BatchPinSet = mock.mock
	return mock, func() { BatchPinSet = originalBatchPinset }
}

// MockFileSystem is a simple mock implementation of FileSystemInterface
type MockFileSystem struct {
	// Expected calls and responses
	readDirCalls     []ReadDirCall
	writeFileCalls   []WriteFileCall
	currentReadDir   int
	currentWriteFile int
}

type ReadDirCall struct {
	expectedPath string
	returnDirs   []os.DirEntry
	returnError  error
}

type WriteFileCall struct {
	expectedPath string
	expectedData []byte
	expectedPerm os.FileMode
	returnError  error
}

func (m *MockFileSystem) ExpectReadDir(path string, dirs []os.DirEntry, err error) {
	m.readDirCalls = append(m.readDirCalls, ReadDirCall{
		expectedPath: path,
		returnDirs:   dirs,
		returnError:  err,
	})
}

func (m *MockFileSystem) ExpectWriteFile(path string, data []byte, perm os.FileMode, err error) {
	m.writeFileCalls = append(m.writeFileCalls, WriteFileCall{
		expectedPath: path,
		expectedData: data,
		expectedPerm: perm,
		returnError:  err,
	})
}

func (m *MockFileSystem) ReadDir(dirname string) ([]os.DirEntry, error) {
	if m.currentReadDir >= len(m.readDirCalls) {
		return nil, errors.New("unexpected ReadDir call")
	}
	call := m.readDirCalls[m.currentReadDir]
	m.currentReadDir++
	// Allow wildcard matching - if expectedPath is empty, accept any path
	if call.expectedPath != "" && call.expectedPath != dirname {
		return nil, errors.New("ReadDir called with unexpected path")
	}
	return call.returnDirs, call.returnError
}

func (m *MockFileSystem) WriteFile(filename string, _ []byte, _ os.FileMode) error {
	if m.currentWriteFile >= len(m.writeFileCalls) {
		return errors.New("unexpected WriteFile call")
	}
	call := m.writeFileCalls[m.currentWriteFile]
	m.currentWriteFile++
	if call.expectedPath != filename {
		return errors.New("WriteFile called with unexpected path")
	}
	return call.returnError
}

func (m *MockFileSystem) VerifyAllCalls(t *testing.T) {
	assert.Equal(t, len(m.readDirCalls), m.currentReadDir, "Not all expected ReadDir calls were made")
	assert.Equal(t, len(m.writeFileCalls), m.currentWriteFile, "Not all expected WriteFile calls were made")
}

// MockDirEntry implements os.DirEntry for testing
type MockDirEntry struct {
	name  string
	isDir bool
}

func (m MockDirEntry) Name() string               { return m.name }
func (m MockDirEntry) IsDir() bool                { return m.isDir }
func (m MockDirEntry) Type() os.FileMode          { return 0 }
func (m MockDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func setupMockFS() (*MockFileSystem, func()) {
	mockFs := MockFileSystem{}
	originalFs := filesystem
	filesystem = &mockFs
	return &mockFs, func() { filesystem = originalFs }
}

func loadProfile(path string) (*ptpv1.PtpProfile, error) {
	profileData, err := os.ReadFile(path)
	if err != nil {
		return &ptpv1.PtpProfile{}, err
	}
	profile := ptpv1.PtpProfile{}
	err = yaml.Unmarshal(profileData, &profile)
	if err != nil {
		return &ptpv1.PtpProfile{}, err
	}
	return &profile, nil
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

func Test_ParseVpd(t *testing.T) {
	b, err := os.ReadFile("./testdata/vpd.bin")
	assert.NoError(t, err)
	vpd := ParseVpd(b)
	assert.Equal(t, "Intel(R) Ethernet Network Adapter E810-XXVDA4T", vpd.VendorSpecific1)
	assert.Equal(t, "2422", vpd.VendorSpecific2)
	assert.Equal(t, "M56954-005", vpd.PartNumber)
	assert.Equal(t, "507C6F1FB174", vpd.SerialNumber)
}

func Test_ProcessProfileTGMNew(t *testing.T) {
	unitTest = true
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	profile, err := loadProfile("./testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	err = OnPTPConfigChangeE810(nil, profile)
	assert.NoError(t, err)
	assert.NotNil(t, mockPinSet.commands, "Ensure clockChain.SetPinDefaults was called")
}

// Test that the profile with no phase inputs is processed correctly
func Test_ProcessProfileTBCNoPhaseInputs(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	unitTest = true

	// Setup filesystem mock for TBC profile - EnableE810Outputs needs this
	mockFS := &MockFileSystem{}
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}

	// EnableE810Outputs reads the ptp directory and writes to SMA2 and period
	mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0666), nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0666), nil)

	// Replace global filesystem with mock
	originalFS := filesystem
	filesystem = mockFS
	defer func() { filesystem = originalFS }()

	profile, err := loadProfile("./testdata/profile-tbc-no-input-delays.yaml")
	assert.NoError(t, err)
	err = OnPTPConfigChangeE810(nil, profile)
	assert.NoError(t, err)

	// Verify that clockChain was initialized (SetPinDefaults is called as part of InitClockChain)
	// If SetPinDefaults wasn't called, InitClockChain would have failed
	assert.NotNil(t, clockChain, "clockChain should be initialized")
	assert.Equal(t, ClockTypeTBC, clockChain.Type, "clockChain should be T-BC type")
	assert.NotNil(t, mockPinSet.commands, "Ensure clockChain.SetPinDefaults was called")

	// Verify all expected filesystem calls were made
	mockFS.VerifyAllCalls(t)
}

func Test_ProcessProfileTbc(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	// Setup filesystem mock for TBC profile (3 devices with pins)
	mockFS := &MockFileSystem{}
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}

	// EnableE810Outputs is called for the leading NIC (ens4f0) - needs specific paths
	mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0666), nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0666), nil)

	// profile-tbc.yaml has pins for ens4f0, ens5f0, ens8f0 (3 devices)
	for i := 0; i < 3; i++ {
		// Each device needs ReadDir + 4 pin writes (SMA1, SMA2, U.FL1, U.FL2)
		mockFS.ExpectReadDir("", phcEntries, nil) // Wildcard path
		for j := 0; j < 4; j++ {
			mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0666), nil)
		}
	}

	// Add extra operations for other calls
	for i := 0; i < 10; i++ {
		mockFS.ExpectReadDir("", phcEntries, nil)                      // Extra ReadDir calls
		mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0666), nil) // Extra WriteFile calls
	}

	// Replace global filesystem with mock
	originalFS := filesystem
	filesystem = mockFS
	defer func() { filesystem = originalFS }()

	// Set unitTest for MockPins() call
	unitTest = true
	defer func() { unitTest = false }()

	// Can read test profile
	profile, err := loadProfile("./testdata/profile-tbc.yaml")
	assert.NoError(t, err)

	// Can run PTP config change handler without errors
	err = OnPTPConfigChangeE810(nil, profile)
	assert.NoError(t, err)
	assert.Equal(t, ClockTypeTBC, clockChain.Type, "identified a wrong clock type")
	assert.Equal(t, "5799633565432596414", clockChain.LeadingNIC.DpllClockID, "identified a wrong clock ID ")
	assert.Equal(t, 9, len(clockChain.LeadingNIC.Pins), "wrong number of configurable pins")
	assert.Equal(t, "ens4f1", clockChain.LeadingNIC.UpstreamPort, "wrong upstream port")

	// Test holdover entry
	mockPinSet.reset()
	err = clockChain.EnterHoldoverTBC()
	assert.NoError(t, err)
	assert.Equal(t, 2, len(*mockPinSet.commands))

	// Test holdover exit
	mockPinSet.reset()
	err = clockChain.EnterNormalTBC()
	assert.NoError(t, err)
	assert.Equal(t, 2, len(*mockPinSet.commands))

	// Ensure switching back to TGM resets any pins
	mockPinSet.reset()
	tgmProfile, err := loadProfile("./testdata/profile-tgm.yaml")
	assert.NoError(t, err)
	err = OnPTPConfigChangeE810(nil, tgmProfile)
	assert.NoError(t, err)
	assert.NotNil(t, mockPinSet.commands, "Ensure clockChain.SetPinDefaults was called")
}

func Test_ProcessProfileTtsc(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	// Setup filesystem mock for T-TSC profile (1 device with pins)
	mockFS := &MockFileSystem{}
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}

	// EnableE810Outputs is called for the leading NIC (ens4f0) - needs specific paths
	mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0666), nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0666), nil)

	// profile-t-tsc.yaml has pins for ens4f0 only
	mockFS.ExpectReadDir("", phcEntries, nil) // One ReadDir
	for i := 0; i < 4; i++ {                  // 4 pin writes
		mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0666), nil)
	}

	// Add extra operations for other calls
	for i := 0; i < 10; i++ {
		mockFS.ExpectReadDir("", phcEntries, nil)                      // Extra ReadDir calls
		mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0666), nil) // Extra WriteFile calls
	}

	// Replace global filesystem with mock
	originalFS := filesystem
	filesystem = mockFS
	defer func() { filesystem = originalFS }()

	// Set unitTest for MockPins() call
	unitTest = true
	defer func() { unitTest = false }()

	// Can read test profile
	profile, err := loadProfile("./testdata/profile-t-tsc.yaml")
	assert.NoError(t, err)

	// Can run PTP config change handler without errors
	err = OnPTPConfigChangeE810(nil, profile)
	assert.NoError(t, err)
	assert.Equal(t, ClockTypeTBC, clockChain.Type, "identified a wrong clock type")
	assert.Equal(t, "5799633565432596414", clockChain.LeadingNIC.DpllClockID, "identified a wrong clock ID ")
	assert.Equal(t, 9, len(clockChain.LeadingNIC.Pins), "wrong number of configurable pins")
	assert.Equal(t, "ens4f1", clockChain.LeadingNIC.UpstreamPort, "wrong upstream port")
	assert.NotNil(t, mockPinSet.commands, "Ensure some pins were set")

	// Test holdover entry
	mockPinSet.reset()
	err = clockChain.EnterHoldoverTBC()
	assert.NoError(t, err)
	assert.Equal(t, 2, len(*mockPinSet.commands))

	// Test holdover exit
	mockPinSet.reset()
	err = clockChain.EnterNormalTBC()
	assert.NoError(t, err)
	assert.Equal(t, 2, len(*mockPinSet.commands))
}

func Test_ProcessProfileTGMOld(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	unitTest = true
	profile, err := loadProfile("./testdata/profile-tgm-old.yaml")
	assert.NoError(t, err)
	err = OnPTPConfigChangeE810(nil, profile)
	assert.NoError(t, err)
	assert.NotNil(t, mockPinSet.commands, "Ensure some pins were set")
}

func Test_SetPinDefaults_AllNICs(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	unitTest = true

	// Setup filesystem mock for EnableE810Outputs
	mockFS := &MockFileSystem{}
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
	mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0666), nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0666), nil)

	// Replace global filesystem with mock
	originalFS := filesystem
	filesystem = mockFS
	defer func() { filesystem = originalFS }()

	// Load a profile with multiple NICs (leading + other NICs)
	profile, err := loadProfile("./testdata/profile-tbc.yaml")
	assert.NoError(t, err)

	// Initialize the clock chain with multiple NICs
	err = OnPTPConfigChangeE810(nil, profile)
	assert.NoError(t, err)

	// Verify we have the expected clock chain structure
	assert.Equal(t, ClockTypeTBC, clockChain.Type)
	assert.Equal(t, "ens4f0", clockChain.LeadingNIC.Name)
	assert.Equal(t, 2, len(clockChain.OtherNICs), "should have 2 other NICs (ens5f0, ens8f0)")

	// Verify each NIC has pins populated
	assert.Greater(t, len(clockChain.LeadingNIC.Pins), 0, "leading NIC should have pins")
	for i, nic := range clockChain.OtherNICs {
		assert.Greater(t, len(nic.Pins), 0, "other NIC %d should have pins", i)
	}

	// Call SetPinDefaults and verify it works with all NICs
	err = clockChain.SetPinDefaults()
	assert.NoError(t, err)
	assert.NotNil(t, mockPinSet.commands)

	// SetPinDefaults configures 9 different pin types, and we have 3 NICs total
	// Each pin type should have a command for each NIC that has that pin
	assert.Equal(t, len(*mockPinSet.commands), 27, "should have exactly 27 pin commands")

	// Verify that commands include pins from multiple clock IDs
	clockIDsSeen := make(map[uint64]bool)
	pinLabelsSeen := make(map[string]bool)

	for _, cmd := range *mockPinSet.commands {
		// Find which pin this command refers to by searching all pins
		for _, pin := range clockChain.DpllPins {
			if pin.ID == cmd.ID {
				clockIDsSeen[pin.ClockID] = true
				pinLabelsSeen[pin.BoardLabel] = true
				break
			}
		}
	}

	// We should see commands for multiple clock IDs (multiple NICs)
	assert.GreaterOrEqual(t, len(clockIDsSeen), 2, "should have commands for at least 2 different clock IDs")

	// We should see commands for the standard configurable pin types
	expectedPins := []string{"GNSS-1PPS", "SMA1", "SMA2/U.FL2", "CVL-SDP20", "CVL-SDP22",
		"CVL-SDP21", "CVL-SDP23", "C827_0-RCLKA", "C827_0-RCLKB"}
	for _, expectedPin := range expectedPins {
		assert.True(t, pinLabelsSeen[expectedPin], "should have command for pin %s", expectedPin)
	}
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
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0666), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0666), nil)
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
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0666), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0666), nil)
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
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0666), errors.New("write failed"))
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
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0666), nil)
				m.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0666), errors.New("period write failed"))
			},
			expectedError: "", // Function doesn't return error for period write failure
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup mock filesystem
			mockFS := &MockFileSystem{}
			tt.setupMock(mockFS)

			// Replace global filesystem with mock
			originalFS := filesystem
			filesystem = mockFS
			defer func() { filesystem = originalFS }()

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
