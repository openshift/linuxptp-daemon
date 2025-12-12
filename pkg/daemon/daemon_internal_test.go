package daemon

// This tests daemon private functions

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bigkevmcd/go-configparser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/yaml"
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
