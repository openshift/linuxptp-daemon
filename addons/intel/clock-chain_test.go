package intel

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_ProcessProfileTbcClockChain(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	// Setup filesystem mock for TBC profile (3 devices with pins)
	mockFS, restoreFs := setupMockFS()
	defer restoreFs()
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}

	// EnableE810Outputs is called for the leading NIC (ens4f0) - needs specific paths
	mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)

	// profile-tbc.yaml has pins for ens4f0, ens5f0, ens8f0 (3 devices)
	for i := 0; i < 3; i++ {
		// Each device needs ReadDir + 4 pin writes (SMA1, SMA2, U.FL1, U.FL2)
		mockFS.ExpectReadDir("", phcEntries, nil) // Wildcard path
		for j := 0; j < 4; j++ {
			mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0o666), nil)
		}
	}

	// Add extra operations for other calls
	for i := 0; i < 10; i++ {
		mockFS.ExpectReadDir("", phcEntries, nil)                       // Extra ReadDir calls
		mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0o666), nil) // Extra WriteFile calls
	}

	// Set unitTest for MockPins() call
	unitTest = true
	defer func() { unitTest = false }()

	// Can read test profile
	profile, err := loadProfile("./testdata/profile-tbc.yaml")
	assert.NoError(t, err)

	// Can run PTP config change handler without errors
	p, d := E810("e810")
	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
	ccData := clockChain.(*ClockChain)
	assert.Equal(t, ClockTypeTBC, ccData.Type, "identified a wrong clock type")
	assert.Equal(t, "5799633565432596414", ccData.LeadingNIC.DpllClockID, "identified a wrong clock ID ")
	assert.Equal(t, 9, len(ccData.LeadingNIC.Pins), "wrong number of configurable pins")
	assert.Equal(t, "ens4f1", ccData.LeadingNIC.UpstreamPort, "wrong upstream port")

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

func Test_ProcessProfileTtscClockChain(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	// Setup filesystem mock for T-TSC profile (1 device with pins)
	mockFS, restoreFs := setupMockFS()
	defer restoreFs()
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}

	// EnableE810Outputs is called for the leading NIC (ens4f0) - needs specific paths
	mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)

	// profile-t-tsc.yaml has pins for ens4f0 only
	mockFS.ExpectReadDir("", phcEntries, nil) // One ReadDir
	for i := 0; i < 4; i++ {                  // 4 pin writes
		mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0o666), nil)
	}

	// Add extra operations for other calls
	for i := 0; i < 10; i++ {
		mockFS.ExpectReadDir("", phcEntries, nil)                       // Extra ReadDir calls
		mockFS.ExpectWriteFile("", []byte(""), os.FileMode(0o666), nil) // Extra WriteFile calls
	}

	// Set unitTest for MockPins() call
	unitTest = true
	defer func() { unitTest = false }()

	// Can read test profile
	profile, err := loadProfile("./testdata/profile-t-tsc.yaml")
	assert.NoError(t, err)

	// Can run PTP config change handler without errors
	p, d := E810("e810")
	err = p.OnPTPConfigChange(d, profile)
	assert.NoError(t, err)
	ccData := clockChain.(*ClockChain)
	assert.Equal(t, ClockTypeTBC, ccData.Type, "identified a wrong clock type")
	assert.Equal(t, "5799633565432596414", ccData.LeadingNIC.DpllClockID, "identified a wrong clock ID ")
	assert.Equal(t, 9, len(ccData.LeadingNIC.Pins), "wrong number of configurable pins")
	assert.Equal(t, "ens4f1", ccData.LeadingNIC.UpstreamPort, "wrong upstream port")
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

func Test_SetPinDefaults_AllNICs(t *testing.T) {
	mockPinSet, restorePinSet := setupBatchPinSetMock()
	defer restorePinSet()
	unitTest = true

	// Setup filesystem mock for EnableE810Outputs
	mockFS, restoreFs := setupMockFS()
	defer restoreFs()
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
	mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA2", []byte("2 2"), os.FileMode(0o666), nil)
	mockFS.ExpectWriteFile("/sys/class/net/ens4f0/device/ptp/ptp0/period", []byte("2 0 0 1 0"), os.FileMode(0o666), nil)

	// Load a profile with multiple NICs (leading + other NICs)
	profile, err := loadProfile("./testdata/profile-tbc.yaml")
	assert.NoError(t, err)

	// Initialize the clock chain with multiple NICs
	err = OnPTPConfigChangeE810(nil, profile)
	assert.NoError(t, err)

	// Verify we have the expected clock chain structure
	ccData := clockChain.(*ClockChain)
	assert.Equal(t, ClockTypeTBC, ccData.Type)
	assert.Equal(t, "ens4f0", ccData.LeadingNIC.Name)
	assert.Equal(t, 2, len(ccData.OtherNICs), "should have 2 other NICs (ens5f0, ens8f0)")

	// Verify each NIC has pins populated
	assert.Greater(t, len(ccData.LeadingNIC.Pins), 0, "leading NIC should have pins")
	for i, nic := range ccData.OtherNICs {
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
		for _, pin := range ccData.DpllPins {
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
	expectedPins := []string{
		"GNSS-1PPS", "SMA1", "SMA2/U.FL2", "CVL-SDP20", "CVL-SDP22",
		"CVL-SDP21", "CVL-SDP23", "C827_0-RCLKA", "C827_0-RCLKB",
	}
	for _, expectedPin := range expectedPins {
		assert.True(t, pinLabelsSeen[expectedPin], "should have command for pin %s", expectedPin)
	}
}
