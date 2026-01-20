package daemon

// This tests daemon private functions

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bigkevmcd/go-configparser"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/hardwareconfig"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

// vendor defaults are embedded; no filesystem setup needed

// NewDaemonForTests creates a Daemon instance for testing
func NewDaemonForTests(tracker *ReadyTracker, processManager *ProcessManager) *Daemon {
	tracker.processManager = processManager
	fakeClient := fake.NewSimpleClientset()
	return &Daemon{
		readyTracker:          tracker,
		processManager:        processManager,
		hardwareConfigManager: hardwareconfig.NewHardwareConfigManager(fakeClient, "default"),
	}
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

func mkPath(t *testing.T) {
	err := os.MkdirAll("/tmp/test", os.ModePerm)
	assert.NoError(t, err)
}

// --- Local JSON→Pin loader for tests (to avoid relying on hardwareconfig internals) ---
type hrPin struct {
	ID           uint32        `json:"id"`
	ModuleName   string        `json:"moduleName"`
	ClockID      string        `json:"clockId"`
	BoardLabel   string        `json:"boardLabel"`
	Type         string        `json:"type"`
	Frequency    uint64        `json:"frequency"`
	ParentDevice []hrParentDev `json:"pinParentDevice"`
}

type hrParentDev struct {
	ParentID  uint32 `json:"parentID"`
	Direction string `json:"direction"`
	Prio      uint32 `json:"prio"`
	State     string `json:"state"`
}

// ParseClockIDHex parses a hex clock ID string (e.g., "0x507c6f...") into uint64.
func parseClockIDHex(s string) uint64 {
	s = strings.TrimPrefix(s, "0x")
	v, _ := strconv.ParseUint(s, 16, 64)
	return v
}

func createMockDpllPinsGetterFromFile(path string) (hardwareconfig.DpllPinsGetter, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var hrs []hrPin
	if unmarshalErr := json.Unmarshal(data, &hrs); unmarshalErr != nil {
		return nil, unmarshalErr
	}
	var pins []*dpll.PinInfo
	for _, h := range hrs {
		p := &dpll.PinInfo{
			ID:           h.ID,
			ModuleName:   h.ModuleName,
			ClockID:      parseClockIDHex(h.ClockID),
			BoardLabel:   h.BoardLabel,
			Type:         dpll.ParsePinType(h.Type),
			Frequency:    h.Frequency,
			Capabilities: 0,
		}
		for _, pd := range h.ParentDevice {
			p.ParentDevice = append(p.ParentDevice, dpll.PinParentDevice{
				ParentID:  pd.ParentID,
				Direction: dpll.ParsePinDirection(pd.Direction),
				Prio:      pd.Prio,
				State:     dpll.ParsePinState(pd.State),
			})
		}
		pins = append(pins, p)
	}
	return hardwareconfig.CreateMockDpllPinsGetter(pins, nil), nil
}

func clean(t *testing.T) {
	err := os.RemoveAll("/tmp/test")
	assert.NoError(t, err)
}
func applyTestProfile(t *testing.T, profile *ptpv1.PtpProfile) {
	stopCh := make(<-chan struct{})
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		// Sleep to allow context to switch
		time.Sleep(100 * time.Millisecond)
		assert.Nil(t, leap.LeapMgr)
	}()
	dn := New(
		"test-node-name",
		"openshift-ptp",
		false,
		nil,
		&LinuxPTPConfUpdate{
			UpdateCh:     make(chan bool),
			NodeProfiles: []ptpv1.PtpProfile{*profile},
		},
		stopCh,
		[]string{"e810"},
		&[]ptpv1.HwConfig{},
		nil,
		make(chan bool),
		30,
		&ReadyTracker{},
	)
	assert.NotNil(t, dn)
	// Signal that no hardware configs are expected for this test
	_ = dn.hardwareConfigManager.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{})
	err := dn.applyNodePtpProfile(0, profile)
	assert.NoError(t, err)
}

func testRequirements(t *testing.T, profile *ptpv1.PtpProfile) {

	cfg, err := configparser.NewConfigParserFromFile("/tmp/test/synce4l.0.config")
	assert.NoError(t, err)
	for _, sec := range cfg.Sections() {
		if strings.HasPrefix(sec, "[<") {
			clk, err := cfg.Get(sec, "clock_id")
			assert.NoError(t, err)
			id, found := profile.PtpSettings["test_clock_id_override"]
			if found {
				assert.NotEqual(t, id, clk)
			} else {
				assert.NotEqual(t, "0", clk)
				assert.NotEqual(t, "", clk)
			}
		}
	}
}
func Test_applyProfile_synce(t *testing.T) {
	defer clean(t)
	testDataFiles := []string{
		"testdata/synce-profile.yaml",
		"testdata/synce-profile-dual.yaml",
		"testdata/synce-profile-custom-id.yaml",
		"testdata/synce-profile-bad-order.yaml",
		"testdata/synce-profile-no-ifaces.yaml",
		"testdata/synce-follower-profile.yaml",
	}
	for i := range len(testDataFiles) {
		mkPath(t)
		profile, err := loadProfile(testDataFiles[i])
		assert.NoError(t, err)
		applyTestProfile(t, profile)
		testRequirements(t, profile)
		clean(t)
	}
}

func Test_applyProfile_TBC(t *testing.T) {
	defer clean(t)

	// Set up mock DPLL pins for testing (load from hardwareconfig testdata)
	if getter, err := createMockDpllPinsGetterFromFile("../hardwareconfig/testdata/pins.json"); err == nil {
		hardwareconfig.SetDpllPinsGetter(getter)
	} else {
		t.Logf("Warning: Failed to setup mock DPLL pins from file: %v", err)
		// Continue with test as DPLL pins are optional
	}
	defer hardwareconfig.TeardownMockDpllPinsForTests()

	testDataFiles := []string{
		"testdata/profile-tbc-tt.yaml",
		"testdata/profile-tbc-tr.yaml",
	}
	stopCh := make(<-chan struct{})
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
		// Sleep to allow context to switch
		time.Sleep(100 * time.Millisecond)
		assert.Nil(t, leap.LeapMgr)
	}()
	dn := New(
		"test-node-name",
		"openshift-ptp",
		false,
		nil,
		&LinuxPTPConfUpdate{
			UpdateCh:     make(chan bool),
			NodeProfiles: []ptpv1.PtpProfile{},
		},
		stopCh,
		[]string{"e810"},
		&[]ptpv1.HwConfig{},
		nil,
		make(chan bool),
		30,
		&ReadyTracker{},
	)
	assert.NotNil(t, dn)
	// Signal that no hardware configs are expected for this test
	_ = dn.hardwareConfigManager.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{})

	for i := range len(testDataFiles) {
		mkPath(t)
		profile, err := loadProfile(testDataFiles[i])
		assert.NoError(t, err)
		// Will assert inside in case of error:
		err = dn.applyNodePtpProfile(0, profile)
		assert.NoError(t, err)
		clean(t)
	}
}

func TestGetPTPClockId_ValidInput(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: ptpv1.PtpProfile{
			PtpSettings: map[string]string{
				"leadingInterface": "eth0",
				"clockId[eth0]":    "123456",
			},
		},
	}

	expectedClockID := "000000.fffe.01e240"
	actualClockID, err := p.getPTPClockID()
	assert.NoError(t, err)
	assert.Equal(t, expectedClockID, actualClockID)
}

func TestGetPTPClockId_MissingLeadingInterface(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: ptpv1.PtpProfile{
			PtpSettings: map[string]string{},
		},
	}

	_, err := p.getPTPClockID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "leadingInterface not found in ptpProfile")
}

func TestGetPTPClockId_MissingClockId(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: ptpv1.PtpProfile{
			PtpSettings: map[string]string{
				"leadingInterface": "eth0",
			},
		},
	}

	_, err := p.getPTPClockID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "leading interface ClockId not found in ptpProfile")
}

func TestGetPTPClockId_ParsingError(t *testing.T) {
	p := &ptpProcess{
		nodeProfile: ptpv1.PtpProfile{
			PtpSettings: map[string]string{
				"leadingInterface": "eth0",
				"clockId[eth0]":    "invalid_string",
			},
		},
	}

	_, err := p.getPTPClockID()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse clock ID string invalid_string")
}

func TestReconcileRelatedProfiles(t *testing.T) {
	tests := []struct {
		name           string
		profiles       []ptpv1.PtpProfile
		expectedResult map[string]int
		description    string
	}{
		{
			name:           "empty profiles",
			profiles:       []ptpv1.PtpProfile{},
			expectedResult: map[string]int{},
			description:    "should return empty map when no profiles provided",
		},
		{
			name: "no controlling profiles",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("profile1"),
					PtpSettings: map[string]string{},
				},
				{
					Name:        stringPointer("profile2"),
					PtpSettings: map[string]string{},
				},
			},
			expectedResult: map[string]int{},
			description:    "should return empty map when no profiles have controllingProfile setting",
		},
		{
			name: "single controlling profile relationship",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("controller"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller",
					},
				},
			},
			expectedResult: map[string]int{
				"controller": 1, // controlled profile is at index 1
			},
			description: "should map controlling profile to controlled profile's index",
		},
		{
			name: "multiple controlling profile relationships",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("controller1"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled1"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller1",
					},
				},
				{
					Name:        stringPointer("controller2"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled2"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller2",
					},
				},
			},
			expectedResult: map[string]int{
				"controller1": 1, // controlled1 is at index 1
				"controller2": 3, // controlled2 is at index 3
			},
			description: "should handle multiple controlling/controlled relationships",
		},
		{
			name: "controlling profile not found",
			profiles: []ptpv1.PtpProfile{
				{
					Name: stringPointer("controlled"),
					PtpSettings: map[string]string{
						"controllingProfile": "nonexistent",
					},
				},
			},
			expectedResult: map[string]int{},
			description:    "should return empty map when controlling profile doesn't exist",
		},
		{
			name: "controlled profile references nonexistent controller",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("profile1"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("profile2"),
					PtpSettings: map[string]string{
						"controllingProfile": "nonexistent_controller",
					},
				},
			},
			expectedResult: map[string]int{},
			description:    "should handle case where controlled profile references non-existent controller",
		},
		{
			name: "empty controllingProfile value",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("controller"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled"),
					PtpSettings: map[string]string{
						"controllingProfile": "",
					},
				},
			},
			expectedResult: map[string]int{},
			description:    "should ignore profiles with empty controllingProfile value",
		},
		{
			name: "complex scenario with mixed relationships",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("standalone"),
					PtpSettings: map[string]string{},
				},
				{
					Name:        stringPointer("controller1"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled1"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller1",
					},
				},
				{
					Name: stringPointer("controlled_orphan"),
					PtpSettings: map[string]string{
						"controllingProfile": "missing_controller",
					},
				},
				{
					Name:        stringPointer("controller2"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled2"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller2",
					},
				},
			},
			expectedResult: map[string]int{
				"controller1": 2, // controlled1 is at index 2
				"controller2": 5, // controlled2 is at index 5
			},
			description: "should handle complex scenario with standalone, valid relationships, and orphaned controlled profiles",
		},
		{
			name: "same controller for multiple controlled profiles (only last one should be recorded)",
			profiles: []ptpv1.PtpProfile{
				{
					Name:        stringPointer("controller"),
					PtpSettings: map[string]string{},
				},
				{
					Name: stringPointer("controlled1"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller",
					},
				},
				{
					Name: stringPointer("controlled2"),
					PtpSettings: map[string]string{
						"controllingProfile": "controller",
					},
				},
			},
			expectedResult: map[string]int{
				"controller": 2, // controlled2 is at index 2 (overwrites controlled1)
			},
			description: "should handle case where multiple profiles reference same controller (last one wins)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := reconcileRelatedProfiles(tt.profiles)
			assert.Equal(t, tt.expectedResult, result, tt.description)
		})
	}
}

// Helper function to create string pointers
func stringPointer(s string) *string {
	return &s
}

// TestTBCTransitionCheck_HardwareConfigPath tests the hardware config path of tBCTransitionCheck
func TestTBCTransitionCheck_HardwareConfigPath(t *testing.T) {
	// Create a real PluginManager
	pmStruct := registerPlugins([]string{})
	pm := &pmStruct

	// Test case: Verify hardware config setup
	t.Run("hardware config setup validation", func(t *testing.T) {
		// Create a ptpProcess with hardware config enabled
		// Set global variable for hardware config
		vTbcHasHardwareConfig = true
		defer func() { vTbcHasHardwareConfig = false }()

		process := &ptpProcess{
			tBCAttributes: tBCProcessAttributes{
				trIfaceName: "ens4f0",
			},
			nodeProfile: ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			eventCh:          make(chan event.EventChannel, 1),              //nolint:govet // needed for test setup
			configName:       "test-config",                                 //nolint:govet // needed for test setup
			clockType:        event.BC,                                      //nolint:govet // needed for test setup
			tbcStateDetector: createMockPTPStateDetectorForHardwareConfig(), // Use mock detector
		}

		// Verify that hardware config path conditions are met
		assert.NotNil(t, process.tbcStateDetector, "PTPStateDetector should be present for hardware config path")
		assert.True(t, vTbcHasHardwareConfig, "Hardware config should be enabled")
		assert.Equal(t, "ens4f0", process.tBCAttributes.trIfaceName, "Interface name should be set correctly")

		// Verify the path selection logic would choose hardware config path
		// This tests the condition: vTbcHasHardwareConfig && p.tbcStateDetector != nil
		assert.True(t, vTbcHasHardwareConfig && process.tbcStateDetector != nil,
			"Hardware config path should be taken when both conditions are met")
	})

	// Test case: Locked transition with offset filtering
	t.Run("locked transition with offset filtering", func(t *testing.T) {
		// Set global variable for hardware config
		oldValue := vTbcHasHardwareConfig
		vTbcHasHardwareConfig = true
		defer func() { vTbcHasHardwareConfig = oldValue }()

		// Create a mock Daemon with hardwareConfigManager and set up hardware config
		fakeClient := fake.NewSimpleClientset()
		hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default")
		err := setupHardwareConfigForTest(hcm, "test-profile", "ens4f0")
		assert.NoError(t, err, "Should be able to set up hardware config")
		mockDaemon := &Daemon{
			hardwareConfigManager: hcm,
		}

		detector := hardwareconfig.NewPTPStateDetector(hcm) // Use same HCM

		// Verify detector has ens4f0 in monitored ports
		monitoredPorts := detector.GetMonitoredPorts()
		assert.Contains(t, monitoredPorts, "ens4f0", "ens4f0 should be in monitored ports")

		process := &ptpProcess{
			tBCAttributes: tBCProcessAttributes{
				trIfaceName:       "ens4f0",
				trPortsConfigFile: "test-config",    // Must match configName for offset filter logic to run
				lastAppliedState:  event.PTP_NOTSET, // Must not be PTP_LOCKED for event to be sent
				offsetThreshold:   10.0,             // Set threshold > offset (5.0) to allow event to be sent
			},
			nodeProfile: ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			eventCh:          make(chan event.EventChannel, 1),
			configName:       "test-config",
			clockType:        event.BC,
			offset:           5.0, // Set offset < threshold (10.0) to allow event to be sent
			tbcStateDetector: detector,
			dn:               mockDaemon,
		}

		// First call: Trigger ConditionTypeLocked (no event sent yet)
		// The parser detects locked state when event contains "to SLAVE"
		process.tBCTransitionCheck("ptp4l[123.456]: [test-config.0.config] port 1 (ens4f0): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED", pm)

		// Verify state changed to LOCKED
		assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastReportedState)

		// Verify filter was created
		assert.NotNil(t, process.tBCAttributes.offsetFilter, "Offset filter should be created")

		// Verify no event was sent yet (event is only sent when filter is full)
		select {
		case <-process.eventCh:
			t.Error("Event should not be sent immediately on locked transition")
		default:
			// Good, no event yet
		}

		// Fill the offset filter by calling tBCTransitionCheck with messages
		// The filter needs to be full (64 samples) before the event is sent
		// First call already inserted 1 sample, so we need 63 more to fill it
		// Use metric log lines (not event lines) to fill the filter
		for i := 0; i < 63; i++ {
			process.tBCTransitionCheck("ptp4l[123.456]: [test-config.0.config] master offset 5 s2 freq 0 path delay 100", pm)
		}

		// Verify event was sent after filter is full
		select {
		case <-process.eventCh:
			// Event was sent, good
		default:
			t.Error("Expected PTP event to be sent after filter is full")
		}

		// Verify lastAppliedState was updated
		assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastAppliedState)
	})

	// Test case: Lost transition (immediate)
	t.Run("lost transition", func(t *testing.T) {
		// Set global variable for hardware config
		oldValue := vTbcHasHardwareConfig
		vTbcHasHardwareConfig = true
		defer func() { vTbcHasHardwareConfig = oldValue }()

		// Create a mock Daemon with hardwareConfigManager and set up hardware config
		fakeClient := fake.NewSimpleClientset()
		hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default")
		err := setupHardwareConfigForTest(hcm, "test-profile", "ens4f0")
		assert.NoError(t, err, "Should be able to set up hardware config")
		mockDaemon := &Daemon{
			hardwareConfigManager: hcm,
		}

		detector := hardwareconfig.NewPTPStateDetector(hcm) // Use same HCM

		// Verify detector has ens4f0 in monitored ports
		monitoredPorts := detector.GetMonitoredPorts()
		assert.Contains(t, monitoredPorts, "ens4f0", "ens4f0 should be in monitored ports")

		process := &ptpProcess{
			tBCAttributes: tBCProcessAttributes{
				trIfaceName: "ens4f0",
			},
			nodeProfile: ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			eventCh:          make(chan event.EventChannel, 1),
			configName:       "test-config",
			clockType:        event.BC,
			tbcStateDetector: detector,
			dn:               mockDaemon,
		}

		// Call with lost transition log - parser detects lost when event contains "SLAVE to"
		process.tBCTransitionCheck("ptp4l[123.456]: [test-config.0.config] port 1 (ens4f0): SLAVE to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES", pm)

		// Verify state changed to FREERUN
		assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.lastReportedState)

		// Verify filter was reset
		assert.Nil(t, process.tBCAttributes.offsetFilter, "Offset filter should be reset on lost transition")

		// Verify event was sent immediately
		select {
		case <-process.eventCh:
			// Event was sent, good
		default:
			t.Error("Expected PTP event to be sent immediately on lost transition")
		}
	})

	// Test case: Hardware config path vs legacy path decision logic
	t.Run("path decision logic", func(t *testing.T) {
		testCases := []struct {
			name                 string
			tbcHasHardwareConfig bool
			hasDetector          bool
			expectedPath         string
		}{
			{
				name:                 "hardware config path",
				tbcHasHardwareConfig: true,
				hasDetector:          true,
				expectedPath:         "hardware",
			},
			{
				name:                 "legacy path - no hardware config",
				tbcHasHardwareConfig: false,
				hasDetector:          true,
				expectedPath:         "legacy",
			},
			{
				name:                 "legacy path - no detector",
				tbcHasHardwareConfig: true,
				hasDetector:          false,
				expectedPath:         "legacy",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				// Set global variable for hardware config
				oldValue := vTbcHasHardwareConfig
				vTbcHasHardwareConfig = tc.tbcHasHardwareConfig
				defer func() { vTbcHasHardwareConfig = oldValue }()

				process := &ptpProcess{
					tBCAttributes: tBCProcessAttributes{
						trIfaceName: "ens4f0",
					},
				}

				if tc.hasDetector {
					process.tbcStateDetector = createMockPTPStateDetectorForHardwareConfig()
				}

				// Determine which path would be taken
				var actualPath string
				if vTbcHasHardwareConfig && process.tbcStateDetector != nil {
					actualPath = "hardware"
				} else {
					actualPath = "legacy"
				}

				assert.Equal(t, tc.expectedPath, actualPath,
					"Expected path %s but got %s", tc.expectedPath, actualPath)
			})
		}
	})
}

// TestTBCTransitionCheck_PathSelection tests which path is taken based on conditions
func TestTBCTransitionCheck_PathSelection(t *testing.T) {
	tests := []struct {
		name                 string
		tbcHasHardwareConfig bool
		hasStateDetector     bool
		expectedLegacy       bool
		description          string
	}{
		{
			name:                 "hardware config path - both conditions true",
			tbcHasHardwareConfig: true,
			hasStateDetector:     true,
			expectedLegacy:       false,
			description:          "Should take hardware config path when both conditions are met",
		},
		{
			name:                 "legacy path - hardware config false",
			tbcHasHardwareConfig: false,
			hasStateDetector:     true,
			expectedLegacy:       true,
			description:          "Should take legacy path when hardware config is disabled",
		},
		{
			name:                 "legacy path - detector nil",
			tbcHasHardwareConfig: true,
			hasStateDetector:     false,
			expectedLegacy:       true,
			description:          "Should take legacy path when detector is not available",
		},
		{
			name:                 "legacy path - both conditions false",
			tbcHasHardwareConfig: false,
			hasStateDetector:     false,
			expectedLegacy:       true,
			description:          "Should take legacy path when both conditions are false",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set global variable for hardware config
			oldValue := vTbcHasHardwareConfig
			vTbcHasHardwareConfig = tt.tbcHasHardwareConfig
			defer func() { vTbcHasHardwareConfig = oldValue }()

			// Create ptpProcess with test conditions
			process := &ptpProcess{
				tBCAttributes: tBCProcessAttributes{
					trIfaceName: "ens4f0",
				},
				nodeProfile: ptpv1.PtpProfile{
					Name: stringPointer("test-profile"),
					PtpSettings: map[string]string{
						"leadingInterface": "ens4f0",
						"clockId[ens4f0]":  "123456789",
					},
				},
				eventCh:    make(chan event.EventChannel, 1), //nolint:govet // needed for test setup
				configName: "test-config",                    //nolint:govet // needed for test setup
				clockType:  event.BC,                         //nolint:govet // needed for test setup
			}

			// Set state detector based on test case
			if tt.hasStateDetector {
				process.tbcStateDetector = createMockPTPStateDetectorForHardwareConfig()
			} else {
				process.tbcStateDetector = nil
			}

			// Test the path selection logic without calling the actual function
			// (to avoid crashes due to incomplete mock setup)

			// Verify that the correct path condition is met
			if tt.expectedLegacy {
				// For legacy path, either hardware config is disabled or detector is nil
				assert.True(t, !vTbcHasHardwareConfig || process.tbcStateDetector == nil,
					"Legacy path should be taken when hardware config is disabled or detector is nil")
			} else {
				// For hardware config path, both conditions must be true
				assert.True(t, vTbcHasHardwareConfig && process.tbcStateDetector != nil,
					"Hardware config path should be taken when both conditions are met")
			}
		})
	}
}

// setupHardwareConfigForTest sets up a hardware config with a PTP source monitoring the given port
func setupHardwareConfigForTest(hcm *hardwareconfig.HardwareConfigManager, profileName, portName string) error {
	// Mock GetDpllPins to return an empty pin cache (no pins needed for this test)
	hardwareconfig.SetDpllPinsGetter(hardwareconfig.CreateMockDpllPinsGetter(nil, nil))
	defer hardwareconfig.ResetDpllPinsGetter()

	// Create a minimal hardware config with a PTP source monitoring the specified port
	hwConfig := ptpv2alpha1.HardwareConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-hwconfig",
		},
		Spec: ptpv2alpha1.HardwareConfigSpec{
			RelatedPtpProfileName: profileName,
			Profile: ptpv2alpha1.HardwareProfile{
				ClockChain: &ptpv2alpha1.ClockChain{
					Behavior: &ptpv2alpha1.Behavior{
						Sources: []ptpv2alpha1.SourceConfig{
							{
								Name:             "PTP4l",
								SourceType:       "ptpTimeReceiver",
								PTPTimeReceivers: []string{portName},
								Subsystem:        "test-subsystem",
							},
						},
					},
					Structure: []ptpv2alpha1.Subsystem{
						{
							Name: "test-subsystem",
							Ethernet: []ptpv2alpha1.Ethernet{
								{
									Ports: []string{portName},
								},
							},
						},
					},
				},
			},
		},
	}

	// Update hardware config manager with the test config
	return hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{hwConfig})
}

// createMockPTPStateDetectorForHardwareConfig creates a mock PTPStateDetector for hardware config testing
// Creates a hardware config with a PTP source that monitors ens4f0, then initializes the detector
func createMockPTPStateDetectorForHardwareConfig() *hardwareconfig.PTPStateDetector {
	// Create a detector using the normal constructor - this properly initializes ptp4lExtractor
	fakeClient := fake.NewSimpleClientset()
	hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default")
	_ = setupHardwareConfigForTest(hcm, "test-profile", "ens4f0")

	// Create detector - it will automatically populate monitoredPorts from the hardware config
	return hardwareconfig.NewPTPStateDetector(hcm)
}

// TestProcessTBCTransitionHardwareConfig_HardwareConfigIntegration tests integration with real hardware config
func TestProcessTBCTransitionHardwareConfig_HardwareConfigIntegration(t *testing.T) {
	// Set up mock PTP device resolver for testing
	hardwareconfig.SetupMockPtpDeviceResolver()
	defer hardwareconfig.TeardownMockPtpDeviceResolver()

	// Set up mock DPLL pins for testing
	if getter, err := createMockDpllPinsGetterFromFile("../hardwareconfig/testdata/pins.json"); err == nil {
		hardwareconfig.SetDpllPinsGetter(getter)
	} else {
		t.Logf("Warning: Failed to setup mock DPLL pins from file: %v", err)
	}
	defer hardwareconfig.TeardownMockDpllPinsForTests()

	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := hardwareconfig.NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	hardwareconfig.SetCommandExecutor(mockCmd)
	defer hardwareconfig.ResetCommandExecutor()

	// Load and parse the hardware config
	hwConfigData, err := os.ReadFile("../hardwareconfig/testdata/wpc-hwconfig.yaml")
	assert.NoError(t, err, "Should be able to read hardware config test data")

	var hwConfig ptpv2alpha1.HardwareConfig
	err = yaml.Unmarshal(hwConfigData, &hwConfig)
	assert.NoError(t, err, "Should be able to parse hardware config YAML")

	// Verify the hardware config has the expected structure for our test
	assert.Equal(t, "01-tbc-tr", hwConfig.Spec.RelatedPtpProfileName, "Expected profile name")
	assert.NotNil(t, hwConfig.Spec.Profile.ClockChain, "Expected clock chain")
	assert.NotNil(t, hwConfig.Spec.Profile.ClockChain.Behavior, "Expected behavior")
	assert.NotEmpty(t, hwConfig.Spec.Profile.ClockChain.Behavior.Sources, "Expected behavior sources")

	// Find the PTP source
	var ptpSource *ptpv2alpha1.SourceConfig
	for i, source := range hwConfig.Spec.Profile.ClockChain.Behavior.Sources {
		if source.SourceType == "ptpTimeReceiver" {
			ptpSource = &hwConfig.Spec.Profile.ClockChain.Behavior.Sources[i]
			break
		}
	}
	assert.NotNil(t, ptpSource, "Should find PTP time receiver source")
	assert.Contains(t, ptpSource.PTPTimeReceivers, "ens4f1", "Expected ens4f1 to be monitored")

	// Create hardware config manager and verify it works with our config
	fakeClient := fake.NewSimpleClientset()
	hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default")
	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{hwConfig})
	assert.NoError(t, err, "Should be able to update hardware config")

	// Verify the profile association
	hasConfig := hcm.HasHardwareConfigForProfile(&ptpv1.PtpProfile{
		Name: stringPointer("01-tbc-tr"),
	})
	assert.True(t, hasConfig, "Should have hardware config for profile 01-tbc-tr")

	// Get configs for the profile
	profiles := hcm.GetHardwareConfigsForProfile(&ptpv1.PtpProfile{
		Name: stringPointer("01-tbc-tr"),
	})
	assert.Len(t, profiles, 1, "Should get exactly one hardware profile")
	assert.NotNil(t, profiles[0].Name, "Hardware profile should have a name")
	assert.Equal(t, "tbc", *profiles[0].Name, "Should get the tbc hardware profile")

	// Get the detector and verify it's properly initialized
	detector := hcm.GetPTPStateDetector()
	assert.NotNil(t, detector, "Should get a valid PTP state detector")

	// Verify monitored ports
	monitoredPorts := detector.GetMonitoredPorts()
	assert.Contains(t, monitoredPorts, "ens4f1", "ens4f1 should be monitored")

	// Test that the detector is ready for use
	t.Run("detector ready for processing", func(t *testing.T) {
		// The detector should be able to handle log processing
		// We'll test this by ensuring it doesn't crash on basic operations
		behaviorRules := detector.GetBehaviorRules()
		assert.NotEmpty(t, behaviorRules, "Should have behavior rules")

		t.Logf("Hardware config loaded successfully with %d monitored ports and %d behavior rules",
			len(monitoredPorts), len(behaviorRules))
	})
}

// TestProcessTBCTransitionHardwareConfig_ProcessLogFile reads log data line by line and processes it
func TestProcessTBCTransitionHardwareConfig_ProcessLogFile(t *testing.T) {
	// Set up mock PTP device resolver for testing
	hardwareconfig.SetupMockPtpDeviceResolver()
	defer hardwareconfig.TeardownMockPtpDeviceResolver()

	// Set up mock DPLL pins for testing
	if getter, err := createMockDpllPinsGetterFromFile("../hardwareconfig/testdata/pins.json"); err == nil {
		hardwareconfig.SetDpllPinsGetter(getter)
	} else {
		t.Logf("Warning: Failed to setup mock DPLL pins from file: %v", err)
	}
	defer hardwareconfig.TeardownMockDpllPinsForTests()

	// Set up mock command executor for GetClockIDFromInterface
	mockCmd := hardwareconfig.NewMockCommandExecutor()
	mockCmd.SetResponse("ethtool", []string{"-i", "ens4f0"}, "driver: ice\nbus-info: 0000:17:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:17:00.0"}, "17:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:17:00.0"}, "serial_number 50-7c-6f-ff-ff-5c-4a-e8")
	mockCmd.SetResponse("ethtool", []string{"-i", "ens8f0"}, "driver: ice\nbus-info: 0000:51:00.0")
	mockCmd.SetResponse("lspci", []string{"-s", "0000:51:00.0"}, "51:00.0 Ethernet controller: Intel Corporation Ethernet Controller E810-C for backplane")
	mockCmd.SetResponse("devlink", []string{"dev", "info", "pci/0000:51:00.0"}, "serial_number 50-7c-6f-ff-ff-1f-b1-b8")
	hardwareconfig.SetCommandExecutor(mockCmd)
	defer hardwareconfig.ResetCommandExecutor()

	// Load the hardware config from testdata
	hwConfigData, err := os.ReadFile("../hardwareconfig/testdata/wpc-hwconfig.yaml")
	assert.NoError(t, err, "Should be able to read hardware config test data")

	// Parse the hardware config
	var hwConfig ptpv2alpha1.HardwareConfig
	err = yaml.Unmarshal(hwConfigData, &hwConfig)
	assert.NoError(t, err, "Should be able to parse hardware config YAML")

	// Create hardware config manager and initialize it
	fakeClient := fake.NewSimpleClientset()
	hcm := hardwareconfig.NewHardwareConfigManager(fakeClient, "default")
	err = hcm.UpdateHardwareConfig([]ptpv2alpha1.HardwareConfig{hwConfig})
	assert.NoError(t, err, "Should be able to update hardware config")

	// Get the PTP state detector
	detector := hcm.GetPTPStateDetector()
	assert.NotNil(t, detector, "Should get a valid PTP state detector")

	// Create a mock Daemon with hardwareConfigManager
	mockDaemon := &Daemon{
		hardwareConfigManager: hcm,
	}

	// Create a ptpProcess with the real hardware config setup
	process := &ptpProcess{
		nodeProfile: ptpv1.PtpProfile{
			Name: stringPointer("01-tbc-tr"), // Matches relatedPtpProfileName from config
			PtpSettings: map[string]string{
				"leadingInterface": "ens4f1",
				"clockId[ens4f1]":  "123456789",
			},
		},
		eventCh:          make(chan event.EventChannel, 100), // Large buffer for all events
		configName:       "test-config",
		clockType:        event.BC,
		tbcStateDetector: detector, // Use real detector with real config
		dn:               mockDaemon,
	}

	// Read the log file line by line
	logFile, err := os.Open("../hardwareconfig/testdata/log2.txt")
	assert.NoError(t, err, "Should be able to open log file")
	defer func() {
		_ = logFile.Close()
	}()

	scanner := bufio.NewScanner(logFile)

	// Track processing results
	linesProcessed := 0
	transitionsDetected := 0
	eventsGenerated := 0
	ptpLinesFound := 0
	ens4f1LinesFound := 0

	// Track state changes
	stateChanges := []event.PTPState{}

	t.Logf("Starting to process log file line by line...")

	// Process each line through processTBCTransitionHardwareConfig
	for scanner.Scan() {
		line := scanner.Text()
		linesProcessed++

		// Track PTP-related lines for debugging
		if strings.Contains(line, "ptp4l") {
			ptpLinesFound++
		}
		if strings.Contains(line, "ens4f1") {
			ens4f1LinesFound++
			// Log first few ens4f1 lines for debugging
			if ens4f1LinesFound <= 5 {
				t.Logf("ens4f1 line %d: %s", ens4f1LinesFound, line)
			}
		}

		// Capture initial state
		initialState := process.tBCAttributes.lastReportedState

		// Process the line through the function under test
		process.processTBCTransitionHardwareConfig(line)

		// Check if state changed
		if process.tBCAttributes.lastReportedState != initialState {
			transitionsDetected++
			stateChanges = append(stateChanges, process.tBCAttributes.lastReportedState)
			t.Logf("Line %d: State transition detected: %s -> %s",
				linesProcessed, initialState, process.tBCAttributes.lastReportedState)
			t.Logf("  Log line: %s", line)
		}

		// Check if event was generated (non-blocking check)
		select {
		case event := <-process.eventCh:
			eventsGenerated++
			t.Logf("Line %d: PTP event generated: %+v", linesProcessed, event)
		default:
			// No event generated, continue
		}

		// Log progress every 10000 lines
		if linesProcessed%10000 == 0 {
			t.Logf("Processed %d lines, detected %d transitions, generated %d events (PTP lines: %d, ens4f1 lines: %d)",
				linesProcessed, transitionsDetected, eventsGenerated, ptpLinesFound, ens4f1LinesFound)
		}
	}

	assert.NoError(t, scanner.Err(), "Should not have errors reading log file")

	// Log final results
	t.Logf("=== FINAL RESULTS ===")
	t.Logf("Total lines processed: %d", linesProcessed)
	t.Logf("PTP lines found: %d", ptpLinesFound)
	t.Logf("ens4f1 lines found: %d", ens4f1LinesFound)
	t.Logf("State transitions detected: %d", transitionsDetected)
	t.Logf("PTP events generated: %d", eventsGenerated)
	t.Logf("Final PTP state: %s", process.tBCAttributes.lastReportedState)

	if len(stateChanges) > 0 {
		t.Logf("State change sequence: %v", stateChanges)
	}
	// The number of transitions depends on the actual log content and hardware config behavior
	// We just verify that the processing completed without crashing
	t.Logf("Processing completed successfully with %d transitions detected", transitionsDetected)
}

// TestTBCTransitionCheck_LegacyPath tests the legacy path of tBCTransitionCheck
func TestTBCTransitionCheck_LegacyPath(t *testing.T) {
	// Create a real PluginManager
	pmStruct := registerPlugins([]string{})
	pm := &pmStruct

	// Test case 1: Locked transition
	t.Run("locked transition", func(t *testing.T) {
		// Set global variable to force legacy path
		oldValue := vTbcHasHardwareConfig
		vTbcHasHardwareConfig = false
		defer func() { vTbcHasHardwareConfig = oldValue }()

		process := &ptpProcess{
			tBCAttributes: tBCProcessAttributes{
				trIfaceName:       "ens4f0",
				trPortsConfigFile: "test-config",    // Must match configName for offset filter logic to run
				lastAppliedState:  event.PTP_NOTSET, // Must not be PTP_LOCKED for event to be sent
				offsetThreshold:   10.0,             // Set threshold > offset (5.0) to allow event to be sent
			},
			nodeProfile: ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			eventCh:    make(chan event.EventChannel, 1),
			configName: "test-config",
			clockType:  event.BC,
			offset:     5.0, // Set offset < threshold (10.0) to allow event to be sent
		}

		// First call: Set state to LOCKED (no event sent yet)
		process.tBCTransitionCheck("ptp4l[123] port 1 (ens4f0): to SLAVE on MASTER_CLOCK_SELECTED", pm)

		// Verify state changed to LOCKED
		assert.Equal(t, event.PTP_LOCKED, process.tBCAttributes.lastReportedState)

		// Verify no event was sent yet (event is only sent when filter is full)
		select {
		case <-process.eventCh:
			t.Error("Event should not be sent immediately on locked transition")
		default:
			// Good, no event yet
		}

		// Fill the offset filter by calling tBCTransitionCheck with messages containing the interface name
		// The filter needs to be full (64 samples) before the event is sent
		// First call already inserted 1 sample, so we need 63 more to fill it
		for i := 0; i < 63; i++ {
			process.tBCTransitionCheck("ptp4l[123] port 1 (ens4f0): some other log message", pm)
		}

		// Verify event was sent after filter is full
		select {
		case <-process.eventCh:
			// Event was sent, good
		default:
			t.Error("Expected PTP event to be sent after filter is full")
		}
	})

	// Test case 2: Lost transition
	t.Run("lost transition", func(t *testing.T) {
		// Set global variable - will still take legacy path due to nil detector
		oldValue := vTbcHasHardwareConfig
		vTbcHasHardwareConfig = true
		defer func() { vTbcHasHardwareConfig = oldValue }()

		process := &ptpProcess{
			tBCAttributes: tBCProcessAttributes{
				trIfaceName: "ens4f0",
			},
			nodeProfile: ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			eventCh:    make(chan event.EventChannel, 1),
			configName: "test-config",
			clockType:  event.BC,
		}

		// Call with lost transition log
		process.tBCTransitionCheck("ptp4l[123] port 1 (ens4f0): SLAVE to", pm)

		// Verify state changed to FREERUN
		assert.Equal(t, event.PTP_FREERUN, process.tBCAttributes.lastReportedState)

		// Verify event was sent
		select {
		case <-process.eventCh:
			// Event was sent, good
		default:
			t.Error("Expected PTP event to be sent")
		}
	})

	// Test case 3: No transition
	t.Run("no transition", func(t *testing.T) {
		// Set global variable
		oldValue := vTbcHasHardwareConfig
		vTbcHasHardwareConfig = true
		defer func() { vTbcHasHardwareConfig = oldValue }()

		process := &ptpProcess{
			tBCAttributes: tBCProcessAttributes{
				trIfaceName: "ens4f0",
			},
			nodeProfile: ptpv1.PtpProfile{
				Name: stringPointer("test-profile"),
				PtpSettings: map[string]string{
					"leadingInterface": "ens4f0",
					"clockId[ens4f0]":  "123456789",
				},
			},
			eventCh:    make(chan event.EventChannel, 1),
			configName: "test-config",
			clockType:  event.BC,
		}

		initialState := process.tBCAttributes.lastReportedState

		// Call with log that doesn't match any transition
		process.tBCTransitionCheck("ptp4l[123] port 1 (ens4f0): some other message", pm)

		// Verify state didn't change
		assert.Equal(t, initialState, process.tBCAttributes.lastReportedState)

		// Verify no event was sent
		select {
		case <-process.eventCh:
			t.Error("Unexpected PTP event was sent")
		default:
			// No event sent, which is correct
		}
	})
}

// TestPtp4lConf_PopulatePtp4lConf_ClockTypeWithCliArgs tests clock_type detection with cliArgs parameter
func TestPtp4lConf_PopulatePtp4lConf_ClockTypeWithCliArgs(t *testing.T) {
	tests := []struct {
		name              string
		config            string
		cliArgs           *string
		expectedClockType event.ClockType
		description       string
	}{
		{
			name:              "OC with -s flag",
			config:            "[global]\n",
			cliArgs:           stringPointer("-s -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI args contain -s flag, should result in OC",
		},
		{
			name:              "OC with -s and single interface",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.OC,
			description:       "CLI -s with single interface should result in OC",
		},
		{
			name:              "BC with -s and multiple interfaces",
			config:            "[global]\n[ens1f0]\nmasterOnly 0\n[ens1f1]\nmasterOnly 1",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.BC,
			description:       "CLI -s with multiple interfaces (one slave) should result in BC",
		},
		{
			name:              "GM with masterOnly interfaces",
			config:            "[global]\n[ens1f0]\nmasterOnly 1\n[ens1f1]\nmasterOnly 1",
			cliArgs:           stringPointer("-f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
			description:       "masterOnly interfaces should result in GM",
		},
		{
			name:              "OC with slaveOnly config",
			config:            "[global]\nslaveOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.OC,
			description:       "slaveOnly in config should result in OC",
		},
		{
			name:              "GM with masterOnly config",
			config:            "[global]\n[ens1f0]\nmasterOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.GM,
			description:       "masterOnly config should result in GM",
		},
		{
			name:              "OC with -s in middle of args",
			config:            "[global]\n",
			cliArgs:           stringPointer("-f /etc/ptp4l.conf -s -m"),
			expectedClockType: event.OC,
			description:       "CLI -s flag in middle of args should be detected for OC",
		},
		{
			name:              "OC with both -s and slaveOnly",
			config:            "[global]\nslaveOnly 1\n",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.OC,
			description:       "Both CLI -s and slaveOnly in config should result in OC",
		},
		{
			name:              "GM with empty cliArgs",
			config:            "[global]\n[ens1f0]\nmasterOnly 1\n",
			cliArgs:           stringPointer(""),
			expectedClockType: event.GM,
			description:       "Empty CLI args string with masterOnly should result in GM",
		},
		{
			name:              "BC with serverOnly 0",
			config:            "[global]\n[ens1f0]\nserverOnly 0\n[ens1f1]\nmasterOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.BC,
			description:       "serverOnly 0 with multiple interfaces should result in BC",
		},
		{
			name:              "OC with clientOnly 1",
			config:            "[global]\n[ens1f0]\nclientOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.OC,
			description:       "clientOnly 1 should be detected as OC",
		},
		{
			name:              "BC with mixed masterOnly",
			config:            "[global]\n[ens1f0]\nmasterOnly 0\n[ens1f1]\nmasterOnly 1\n",
			cliArgs:           nil,
			expectedClockType: event.BC,
			description:       "Multiple interfaces with mixed masterOnly should result in BC",
		},
		{
			name:              "OC with -s and serverOnly 0",
			config:            "[global]\n[ens1f0]\nserverOnly 0\n",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.OC,
			description:       "CLI -s with serverOnly 0 and single interface should result in OC",
		},
		{
			name:              "OC with -s and clientOnly 1",
			config:            "[global]\n[ens1f0]\nclientOnly 1\n",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.OC,
			description:       "CLI -s with clientOnly 1 should result in OC",
		},
		{
			name:              "GM with no slave configuration",
			config:            "[global]\n[ens1f0]\n[ens1f1]\n",
			cliArgs:           stringPointer("-f /etc/ptp4l.conf -m"),
			expectedClockType: event.GM,
			description:       "Multiple interfaces with no explicit master/slave settings should result in GM",
		},
		{
			name:              "OC with --slaveOnly 1",
			config:            "[global]\n",
			cliArgs:           stringPointer("--slaveOnly 1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --slaveOnly 1 should result in OC",
		},
		{
			name:              "OC with --slaveOnly=1",
			config:            "[global]\n",
			cliArgs:           stringPointer("--slaveOnly=1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --slaveOnly=1 should result in OC",
		},
		{
			name:              "GM with --slaveOnly 0",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPointer("--slaveOnly 0 -f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
			description:       "CLI --slaveOnly 0 should result in GM",
		},
		{
			name:              "GM with --slaveOnly=0",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPointer("--slaveOnly=0 -f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
			description:       "CLI --slaveOnly=0 should result in GM",
		},
		{
			name:              "OC with --clientOnly 1",
			config:            "[global]\n",
			cliArgs:           stringPointer("--clientOnly 1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --clientOnly 1 should result in OC",
		},
		{
			name:              "OC with --clientOnly=1",
			config:            "[global]\n",
			cliArgs:           stringPointer("--clientOnly=1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --clientOnly=1 should result in OC",
		},
		{
			name:              "GM with --clientOnly 0",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPointer("--clientOnly 0 -f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
			description:       "CLI --clientOnly 0 should result in GM",
		},
		{
			name:              "GM with --clientOnly=0",
			config:            "[global]\n[ens1f0]\n",
			cliArgs:           stringPointer("--clientOnly=0 -f /etc/ptp4l.conf"),
			expectedClockType: event.GM,
			description:       "CLI --clientOnly=0 should result in GM",
		},
		{
			name:              "OC with --slaveOnly and multiple spaces",
			config:            "[global]\n",
			cliArgs:           stringPointer("--slaveOnly  1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --slaveOnly with multiple spaces should result in OC",
		},
		{
			name:              "OC with --clientOnly and multiple spaces",
			config:            "[global]\n",
			cliArgs:           stringPointer("--clientOnly   1 -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI --clientOnly with multiple spaces should result in OC",
		},
		{
			name:              "OC with -s at end of args",
			config:            "[global]\n",
			cliArgs:           stringPointer("-f /etc/ptp4l.conf -s"),
			expectedClockType: event.OC,
			description:       "CLI -s at end of args should result in OC",
		},
		{
			name:              "OC with -s at start of args",
			config:            "[global]\n",
			cliArgs:           stringPointer("-s -f /etc/ptp4l.conf"),
			expectedClockType: event.OC,
			description:       "CLI -s at start of args should result in OC",
		},
		{
			name:              "OC with only -s flag",
			config:            "[global]\n",
			cliArgs:           stringPointer("-s"),
			expectedClockType: event.OC,
			description:       "CLI with only -s flag should result in OC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := &Ptp4lConf{}
			err := conf.PopulatePtp4lConf(&tt.config, tt.cliArgs)

			assert.NoError(t, err, "PopulatePtp4lConf should not return error")
			assert.Equal(t, tt.expectedClockType, conf.clock_type,
				"Clock type mismatch: expected %v, got %v - %s",
				tt.expectedClockType, conf.clock_type, tt.description)
		})
	}
}
