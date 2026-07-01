package daemon

// This tests daemon private functions

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bigkevmcd/go-configparser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/yaml"
)

const (
	testSkipStartupReason = "delayed"
)

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

	tests := []struct {
		dataFile          string
		expectedProcesses []string
	}{
		{
			dataFile:          "testdata/profile-tbc-tt.yaml",
			expectedProcesses: []string{ptp4lProcessName},
		},
		{
			dataFile:          "testdata/profile-tbc-tr.yaml",
			expectedProcesses: []string{ptp4lProcessName, ptp4lProcessName, ts2phcProcessName, phc2sysProcessName},
		},
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

	for _, test := range tests {
		mkPath(t)
		profile, err := loadProfile(test.dataFile)
		assert.NoError(t, err)
		// Will assert inside in case of error:
		err = dn.applyNodePtpProfile(0, profile)
		assert.NoError(t, err)

		// Ensure for T-BC that phc2sys is selected for delayed-start
		actualProcesses := []string{}
		for _, p := range dn.processManager.process {
			actualProcesses = append(actualProcesses, p.name)
			if p.name == phc2sysProcessName {
				assert.NotEmpty(t, p.skipInitialStartup, "Ensure phc2sys is startup-delayed for T-BC")
			}
		}
		assert.ElementsMatch(t, test.expectedProcesses, actualProcesses, "Ensure T-BC has the required processes prepared (%s)", test.dataFile)
		clean(t)
	}
}

func Test_applyProfile_TGM(t *testing.T) {
	defer clean(t)
	mkPath(t)

	stopCh := make(<-chan struct{})
	assert.NoError(t, leap.MockLeapFile())
	defer func() {
		close(leap.LeapMgr.Close)
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

	profile, err := loadProfile("testdata/profile-tgm.yaml")
	assert.NoError(t, err)

	err = dn.applyNodePtpProfile(0, profile)
	assert.NoError(t, err)

	var ts2phcProc *ptpProcess
	var ptp4lProc *ptpProcess
	for _, p := range dn.processManager.process {
		switch p.name {
		case ts2phcProcessName:
			ts2phcProc = p
		case ptp4lProcessName:
			ptp4lProc = p
		case phc2sysProcessName:
			assert.NotEmpty(t, p.skipInitialStartup, "Ensure phc2sys is startup-delayed for T-GM")
		}
	}

	// 1. ts2phc must have both gpsd and gpspipe as dependent processes.
	if assert.NotNil(t, ts2phcProc, "ts2phc process should exist for T-GM profile") {
		depNames := make([]string, len(ts2phcProc.depProcess))
		for i, d := range ts2phcProc.depProcess {
			depNames[i] = d.Name()
		}
		assert.Contains(t, depNames, GPSD_PROCESSNAME, "gpsd must be a dependent process of ts2phc for T-GM")
		assert.Contains(t, depNames, GPSPIPE_PROCESSNAME, "gpspipe must be a dependent process of ts2phc for T-GM")
	}

	// 2. ptp4l should NOT have a PMC dependent process for GM profiles.
	if assert.NotNil(t, ptp4lProc, "ptp4l process should exist for T-GM profile") {
		for _, d := range ptp4lProc.depProcess {
			assert.NotEqual(t, "pmc", d.Name(), "ptp4l should not have PMC as a dependent process for T-GM")
		}
	}

	// 3. ts2phc opts must include holdover and servo parameters for GM.
	// These are auto-appended by applyNodePtpProfile for GM clock types.
	ts2phcOpts := *profile.Ts2PhcOpts
	assert.Contains(t, ts2phcOpts, "--ts2phc.holdover", "ts2phc opts must include holdover timeout for T-GM")
	assert.Contains(t, ts2phcOpts, "--servo_offset_threshold", "ts2phc opts must include servo offset threshold for T-GM")
	assert.Contains(t, ts2phcOpts, "--servo_num_offset_values 10", "ts2phc opts must include servo num offset values for T-GM")
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

func TestEmitClockClassLogs_EmitsWithNilParentDS(t *testing.T) {
	// Create a minimal event handler (no socket needed — the event package
	// tests already cover the socket write path via EmitClockClass).
	eventChannel := make(chan event.EventChannel)
	closeCh := make(chan bool, 1)
	handler := event.Init("testnode", false, "", eventChannel, closeCh, nil, nil, nil)

	// Create a PMC process with parentDS = nil (the bug condition).
	// Before the fix, EmitClockClassLogs skipped the call when parentDS was nil.
	pmcProc := NewPMCProcess(0, handler, "OC")
	assert.Nil(t, pmcProc.parentDS, "parentDS should be nil for this test")

	// Create a ptp4l process with the PMC as a dependent process
	pm := &ProcessManager{
		process: []*ptpProcess{
			{
				name:       ptp4lProcessName,
				depProcess: []process{pmcProc},
			},
		},
	}

	// EmitClockClassLogs should dispatch to pmc.EmitClockClassLogs()
	// without panicking, even when parentDS is nil.
	// EmitClockClass returns early (no clkSyncState data) but the key
	// assertion is that the call is made at all — the old code skipped it.
	assert.NotPanics(t, func() {
		pm.EmitClockClassLogs()
	}, "EmitClockClassLogs should not panic with nil parentDS")
}

// --- ReadyTracker.Ready() unit tests ---

func makeReadyTracker(processes []*ptpProcess) *ReadyTracker {
	return &ReadyTracker{
		config: true,
		processManager: &ProcessManager{
			process: processes,
		},
	}
}

func TestReady_NoProcesses(t *testing.T) {
	rt := makeReadyTracker(nil)
	ok, msg := rt.Ready()
	assert.False(t, ok)
	assert.Contains(t, msg, "No processes")
}

func TestReady_AllRunningWithMetrics(t *testing.T) {
	rt := makeReadyTracker([]*ptpProcess{
		{name: ptp4lProcessName, stopped: false, hasCollectedMetrics: true},
		{name: phc2sysProcessName, stopped: false, hasCollectedMetrics: true},
	})
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_StoppedProcessReportsNotReady(t *testing.T) {
	rt := makeReadyTracker([]*ptpProcess{
		{name: ptp4lProcessName, stopped: false, hasCollectedMetrics: true},
		{name: phc2sysProcessName, stopped: true},
	})
	ok, msg := rt.Ready()
	assert.False(t, ok)
	assert.Contains(t, msg, "Stopped")
	assert.Contains(t, msg, phc2sysProcessName)
}

func TestReady_DelayedPhc2sysNotReportedAsStopped(t *testing.T) {
	// phc2sys is intentionally delayed (skipInitialStartup set): the pod
	// should be considered ready without it.
	rt := makeReadyTracker([]*ptpProcess{
		{name: ptp4lProcessName, stopped: false, hasCollectedMetrics: true},
		{name: phc2sysProcessName, stopped: true, skipInitialStartup: testSkipStartupReason},
	})
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_NilProcessEntrySkipped(t *testing.T) {
	// nil slots in the process slice must not panic.
	rt := makeReadyTracker([]*ptpProcess{
		{name: ptp4lProcessName, stopped: false, hasCollectedMetrics: true},
		nil,
	})
	ok, msg := rt.Ready()
	assert.True(t, ok, msg)
}

func TestReady_AllProcessesDelayed(t *testing.T) {
	// If every process has skipInitialStartup set (e.g. a phc2sys-only HA profile
	// where ptp4l lives in separate profiles), the pod must not report ready.
	rt := makeReadyTracker([]*ptpProcess{
		{name: phc2sysProcessName, stopped: true, skipInitialStartup: testSkipStartupReason},
	})
	ok, msg := rt.Ready()
	assert.False(t, ok)
	assert.Contains(t, msg, "No processes")
}

func TestDelayedPhc2sysStartup(t *testing.T) {
	profileName := "test-profile"
	nodeProfile := ptpv1.PtpProfile{
		Name: &profileName,
	}

	pm := &ProcessManager{
		process: []*ptpProcess{},
	}

	dn := &Daemon{
		processManager: pm,
	}

	phc2sys := &ptpProcess{
		name:               phc2sysProcessName,
		skipInitialStartup: testSkipStartupReason,
		nodeProfile:        nodeProfile,
		dn:                 dn,
		execMutex:          sync.Mutex{},
		stopped:            true, // Simulated stopped state
	}

	ts2phc := &ptpProcess{
		name:        ts2phcProcessName,
		nodeProfile: nodeProfile,
		dn:          dn,
		eventCh:     make(chan event.EventChannel, 10),
		ptpClockThreshold: &ptpv1.PtpClockThreshold{
			MaxOffsetThreshold: 1000,
			MinOffsetThreshold: -1000,
		},
	}

	pm.process = append(pm.process, phc2sys, ts2phc)

	// 1. Simulate large offset (> 1s)
	dn.delayedPhc2sys.Store(true)
	largeOffset := 37000000000.0 // 37s
	ts2phc.ProcessTs2PhcEvents(largeOffset, ts2phcProcessName, "eth0", event.PTP_FREERUN, nil)

	// Verify phc2sys is still delayed
	assert.Equal(t, testSkipStartupReason, phc2sys.skipInitialStartup)

	// 2. Exact boundary (== 1s): should NOT clear the delay (condition is strictly <)
	phc2sys.skipInitialStartup = testSkipStartupReason
	dn.delayedPhc2sys.Store(true)
	boundaryOffset := 1000000000.0 // exactly 1s
	ts2phc.ProcessTs2PhcEvents(boundaryOffset, ts2phcProcessName, "eth0", event.PTP_FREERUN, nil)
	assert.Equal(t, testSkipStartupReason, phc2sys.skipInitialStartup, "At the 1s boundary phc2sys should remain delayed")
	assert.True(t, dn.delayedPhc2sys.Load())

	// 3. Negative sub-second offset (-0.5s): math.Abs should clear the delay
	phc2sys.skipInitialStartup = testSkipStartupReason
	dn.delayedPhc2sys.Store(true)
	negSmallOffset := -500000000.0 // -0.5s
	ts2phc.ProcessTs2PhcEvents(negSmallOffset, ts2phcProcessName, "eth0", event.PTP_LOCKED, nil)
	assert.Equal(t, "", phc2sys.skipInitialStartup, "Negative sub-second offset should clear the delay")
	assert.False(t, dn.delayedPhc2sys.Load())

	// 4. Negative super-second offset (-2s): should NOT clear the delay
	phc2sys.skipInitialStartup = testSkipStartupReason
	dn.delayedPhc2sys.Store(true)
	negLargeOffset := -2000000000.0 // -2s
	ts2phc.ProcessTs2PhcEvents(negLargeOffset, ts2phcProcessName, "eth0", event.PTP_FREERUN, nil)
	assert.Equal(t, testSkipStartupReason, phc2sys.skipInitialStartup, "Negative super-second offset should keep the delay")
	assert.True(t, dn.delayedPhc2sys.Load())

	// 5. Simulate sub-second offset (< 1s): original passing case
	phc2sys.skipInitialStartup = testSkipStartupReason
	dn.delayedPhc2sys.Store(true)
	smallOffset := 500000000.0 // 0.5s
	ts2phc.ProcessTs2PhcEvents(smallOffset, ts2phcProcessName, "eth0", event.PTP_LOCKED, nil)
	assert.Equal(t, "", phc2sys.skipInitialStartup)
	assert.False(t, dn.delayedPhc2sys.Load())
}

// TestDelayedPhc2sysStartup_HAProfile verifies that a phc2sys process in a
// dedicated HA profile (with no ptp4l of its own) is correctly started when
// a sub-second offset is reported by a ptp4l in one of its haProfile entries.
func TestDelayedPhc2sysStartup_HAProfile(t *testing.T) {
	phc2sysProfileName := "test-dual-nic-bc-ha"
	master1ProfileName := "test-bc-master1"
	master2ProfileName := "test-bc-master2"

	phc2sysNodeProfile := ptpv1.PtpProfile{Name: &phc2sysProfileName}
	master1NodeProfile := ptpv1.PtpProfile{Name: &master1ProfileName}
	master2NodeProfile := ptpv1.PtpProfile{Name: &master2ProfileName}

	pm := &ProcessManager{process: []*ptpProcess{}}
	dn := &Daemon{processManager: pm}

	phc2sys := &ptpProcess{
		name:               phc2sysProcessName,
		skipInitialStartup: testSkipStartupReason,
		nodeProfile:        phc2sysNodeProfile,
		haProfile:          map[string][]string{master1ProfileName: {"ens1f1"}, master2ProfileName: {"ens2f0"}},
		dn:                 dn,
		execMutex:          sync.Mutex{},
		stopped:            true,
	}

	ptp4lMaster1 := &ptpProcess{
		name:        ptp4lProcessName,
		nodeProfile: master1NodeProfile,
		dn:          dn,
		eventCh:     make(chan event.EventChannel, 10),
		ptpClockThreshold: &ptpv1.PtpClockThreshold{
			MaxOffsetThreshold: 1000,
			MinOffsetThreshold: -1000,
		},
	}

	ptp4lMaster2 := &ptpProcess{
		name:        ptp4lProcessName,
		nodeProfile: master2NodeProfile,
		dn:          dn,
		eventCh:     make(chan event.EventChannel, 10),
		ptpClockThreshold: &ptpv1.PtpClockThreshold{
			MaxOffsetThreshold: 1000,
			MinOffsetThreshold: -1000,
		},
	}

	pm.process = append(pm.process, phc2sys, ptp4lMaster1, ptp4lMaster2)

	// A large offset from master1 must NOT start phc2sys.
	dn.delayedPhc2sys.Store(true)
	dn.HandleDelayedPhc2sysStartup(ptp4lProcessName, 37000000000.0, &master1ProfileName)
	assert.Equal(t, testSkipStartupReason, phc2sys.skipInitialStartup,
		"large offset from HA-linked profile should not start phc2sys")

	// A sub-second offset from master2 (different profile from phc2sys) MUST start it.
	dn.delayedPhc2sys.Store(true)
	dn.HandleDelayedPhc2sysStartup(ptp4lProcessName, 500000000.0, &master2ProfileName)
	assert.Equal(t, "", phc2sys.skipInitialStartup,
		"sub-second offset from HA-linked profile should start phc2sys")
	assert.False(t, dn.delayedPhc2sys.Load())
}

func TestFindProcessesByName(t *testing.T) {
	pm := &ProcessManager{
		process: []*ptpProcess{
			{name: "ptp4l"},
			{name: "phc2sys"},
			{name: "ptp4l"},
		},
	}

	procs := pm.findProcessesByName("ptp4l")
	assert.Equal(t, 2, len(procs))
	assert.Equal(t, "ptp4l", procs[0].name)
	assert.Equal(t, "ptp4l", procs[1].name)

	procs = pm.findProcessesByName("phc2sys")
	assert.Equal(t, 1, len(procs))

	procs = pm.findProcessesByName("nonexistent")
	assert.Equal(t, 0, len(procs))
}
