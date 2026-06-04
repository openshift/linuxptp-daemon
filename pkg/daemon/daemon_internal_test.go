package daemon

// This tests daemon private functions

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bigkevmcd/go-configparser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/yaml"
)

const testPtp4lOffsetLine = "ptp4l[1.000]: [ptp4l.0.config] master offset 5 s2 freq -1000 path delay 100"

// NewDaemonForTests creates a Daemon instance for testing
func NewDaemonForTests(tracker *ReadyTracker, processManager *ProcessManager) *Daemon {
	tracker.processManager = processManager
	return &Daemon{
		readyTracker:   tracker,
		processManager: processManager,
		liveGate:       &liveGate{},
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

// --- liveGate unit tests ---

func TestLiveGate_NilGateReturnsImmediately(t *testing.T) {
	dn := &Daemon{
		liveGate: &liveGate{},
	}
	assert.Nil(t, dn.liveGate.c, "nil gate means disabled")
	dn.liveGate.Wait(liveGateTimeout)
}

func TestLiveGate_OpenBeforeWait(t *testing.T) {
	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()
	assert.NotNil(t, dn.liveGate.c, "gate should exist after reset")
	dn.liveGate.Open()

	dn.liveGate.Wait(liveGateTimeout)
}

func TestLiveGate_WaitThenOpen(t *testing.T) {
	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()

	result := make(chan bool, 1)
	go func() {
		dn.liveGate.Wait(liveGateTimeout)
		result <- true
	}()

	// Gate is closed — result should not be ready yet
	select {
	case <-result:
		t.Fatal("waitForLiveGate returned before gate was opened")
	case <-time.After(50 * time.Millisecond):
	}

	dn.liveGate.Open()

	select {
	case r := <-result:
		assert.True(t, r, "gate opened, should return true")
	case <-time.After(2 * time.Second):
		t.Fatal("waitForLiveGate did not return after openLiveGate")
	}
}

func TestLiveGate_ClosedGateBlocksUntilTimeout(t *testing.T) {
	origTimeout := liveGateTimeout
	liveGateTimeout = 80 * time.Millisecond
	defer func() { liveGateTimeout = origTimeout }()

	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()

	start := time.Now()
	dn.liveGate.Wait(liveGateTimeout)
	assert.GreaterOrEqual(t, time.Since(start).Milliseconds(), int64(70),
		"should block for approximately the timeout duration")
}

func TestLiveGate_DoubleOpenIsSafe(t *testing.T) {
	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()
	dn.liveGate.Open()
	assert.NotPanics(t, func() { dn.liveGate.Open() }, "double open must not panic")

	dn.liveGate.Wait(liveGateTimeout)
}

func TestLiveGate_ResetReblocks(t *testing.T) {
	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()
	dn.liveGate.Open()

	dn.liveGate.Wait(liveGateTimeout)

	// Reset creates a new blocked gate
	dn.liveGate.Reset()

	result := make(chan bool, 1)
	go func() {
		dn.liveGate.Wait(liveGateTimeout)
		result <- true
	}()

	select {
	case <-result:
		t.Fatal("waitForLiveGate returned before re-opened gate")
	case <-time.After(50 * time.Millisecond):
	}

	dn.liveGate.Open()

	select {
	case r := <-result:
		assert.True(t, r)
	case <-time.After(2 * time.Second):
		t.Fatal("waitForLiveGate did not return after second openLiveGate")
	}
}

func TestLiveGate_MultipleWaitersAllUnblock(t *testing.T) {
	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()

	const n = 5
	results := make(chan bool, n)
	for i := 0; i < n; i++ {
		go func() {
			dn.liveGate.Wait(liveGateTimeout)
			results <- true
		}()
	}

	// None should have returned yet
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, len(results), "no waiter should return before gate opens")

	dn.liveGate.Open()

	for i := 0; i < n; i++ {
		select {
		case r := <-results:
			assert.True(t, r)
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d did not return after openLiveGate", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration tests: full daemon ↔ CEP communication over the event socket
// ---------------------------------------------------------------------------
//
// These tests simulate both sides of the live-gate protocol to verify the
// complete flow works without deadlock:
//
//  Daemon side:
//    - Scanner goroutine: drains "ptp4l stdout" (io.Pipe) immediately
//    - Socket-writer goroutine: connects to CEP socket, waits for liveGate,
//      sends CMD LIVE_START, then forwards lines from lineCh
//    - /emit-logs HTTP handler: opens the liveGate (and optionally writes
//      replay data through a separate EventHandler connection)
//
//  CEP side (simulated from cloud-event-proxy processMessages logic):
//    - Unix socket listener accepts connections
//    - For each connection: calls TriggerLogs (HTTP GET to /emit-logs),
//      then scans messages from the socket

func shortTestSocketPath(t *testing.T) string {
	t.Helper()
	// Use os.MkdirTemp with a short prefix to stay within the 104-byte
	// Unix socket path limit on macOS. t.TempDir() embeds the full test
	// name, which can exceed this limit for long test names.
	dir, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatalf("shortTestSocketPath: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir + "/e.sock"
}

// TestLiveGateIntegration_FullDaemonCEPFlow simulates the complete ptp4l →
// linuxptp-daemon → CEP flow: scanner goroutine drains stdout, socket-writer
// waits for the live gate, CEP triggers /emit-logs (which opens the gate),
// then CEP receives CMD LIVE_START followed by all live ptp4l lines.
func TestLiveGateIntegration_FullDaemonCEPFlow(t *testing.T) {
	socketPath := shortTestSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()

	// Daemon HTTP server (/emit-logs opens the gate)
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("/emit-logs", func(w http.ResponseWriter, _ *http.Request) {
		dn.liveGate.Open()
		w.WriteHeader(http.StatusOK)
	})
	httpSrv := &http.Server{Handler: mux}
	go func() { _ = httpSrv.Serve(httpLn) }()
	defer httpSrv.Close()
	triggerURL := "http://" + httpLn.Addr().String() + "/emit-logs"

	// CEP side: accept ONE connection, call TriggerLogs, read messages.
	cepMsgs := make(chan []string, 1)
	go func() {
		fd, acceptErr := listener.Accept()
		if acceptErr != nil {
			cepMsgs <- nil
			return
		}
		defer fd.Close()

		// Simulate processMessages: TriggerLogs then scan.
		client := &http.Client{Timeout: 5 * time.Second}
		resp, trigErr := client.Get(triggerURL)
		if trigErr != nil {
			t.Logf("CEP TriggerLogs failed: %v", trigErr)
			cepMsgs <- nil
			return
		}
		_ = resp.Body.Close()

		var msgs []string
		scanner := bufio.NewScanner(fd)
		for scanner.Scan() {
			msgs = append(msgs, scanner.Text())
		}
		cepMsgs <- msgs
	}()

	// Daemon side: simulated ptp4l stdout → scanner goroutine → lineCh
	ptp4lR, ptp4lW := io.Pipe()
	lineCh := make(chan string, 256)
	scannerDone := make(chan struct{})
	writerDone := make(chan struct{})

	livePtp4lLines := []string{
		testPtp4lOffsetLine,
		"ptp4l[2.000]: [ptp4l.0.config] master offset 3 s2 freq -998 path delay 99",
		"ptp4l[3.000]: [ptp4l.0.config] master offset -1 s2 freq -995 path delay 101",
		"ptp4l[4.000]: [ptp4l.0.config] selected best master clock 001122.fffe.334455",
		"ptp4l[5.000]: [ptp4l.0.config] master offset 2 s2 freq -997 path delay 100",
	}

	// Scanner goroutine (daemon)
	go func() {
		defer close(scannerDone)
		s := bufio.NewScanner(ptp4lR)
		for s.Scan() {
			select {
			case lineCh <- s.Text():
			default:
			}
		}
		close(lineCh)
	}()

	// Socket-writer goroutine (daemon)
	go func() {
		defer close(writerDone)
		c, dialErr := net.DialTimeout("unix", socketPath, 5*time.Second)
		if dialErr != nil {
			t.Errorf("socket-writer dial failed: %v", dialErr)
			return
		}
		defer c.Close()

		dn.liveGate.Wait(liveGateTimeout)
		if _, wErr := fmt.Fprintf(c, "%s\n", liveStartCommand); wErr != nil {
			t.Errorf("LIVE_START write error: %v", wErr)
			return
		}
		for line := range lineCh {
			if _, wErr := fmt.Fprintf(c, "%s\n", line); wErr != nil {
				t.Errorf("live data write error: %v", wErr)
				return
			}
		}
	}()

	// Feed simulated ptp4l output
	go func() {
		for _, line := range livePtp4lLines {
			fmt.Fprintln(ptp4lW, line)
			time.Sleep(5 * time.Millisecond)
		}
		ptp4lW.Close()
	}()

	// Wait for daemon goroutines
	select {
	case <-scannerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: scanner goroutine did not finish")
	}
	select {
	case <-writerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: socket-writer goroutine did not finish — possible deadlock")
	}

	// Verify CEP received everything
	select {
	case msgs := <-cepMsgs:
		if !assert.NotNil(t, msgs, "CEP should have received messages") {
			return
		}
		t.Logf("CEP received %d messages", len(msgs))
		assert.GreaterOrEqual(t, len(msgs), 1+len(livePtp4lLines),
			"should receive LIVE_START + all ptp4l lines")
		assert.Equal(t, liveStartCommand, msgs[0], "first message should be CMD LIVE_START")

		foundBMC := false
		for _, m := range msgs {
			if strings.Contains(m, "selected best master clock") {
				foundBMC = true
			}
		}
		assert.True(t, foundBMC, "live data must contain 'selected best master clock'")
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: CEP did not finish reading messages")
	}
}

// TestLiveGateIntegration_ScannerDrainsWhileGateBlocked verifies that the
// scanner goroutine keeps draining ptp4l stdout even while the socket-writer
// is blocked waiting for the live gate. This was the original bug: a single
// goroutine both waited for the gate AND read stdout, causing ptp4l to freeze.
func TestLiveGateIntegration_ScannerDrainsWhileGateBlocked(t *testing.T) {
	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset() // gate is CLOSED

	ptp4lR, ptp4lW := io.Pipe()
	lineCh := make(chan string, 256)
	scannerDone := make(chan struct{})

	// Scanner goroutine: must drain immediately regardless of gate state.
	go func() {
		defer close(scannerDone)
		s := bufio.NewScanner(ptp4lR)
		for s.Scan() {
			select {
			case lineCh <- s.Text():
			default:
			}
		}
		close(lineCh)
	}()

	// Write lines while gate is CLOSED. If scanner were gated, ptp4lW.Write
	// would block (pipe full) and this goroutine would hang.
	writeCount := 100
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for i := 0; i < writeCount; i++ {
			fmt.Fprintf(ptp4lW, "ptp4l[%d.000]: [ptp4l.0.config] master offset %d s2 freq 0 path delay 100\n", i, i)
		}
		ptp4lW.Close()
	}()

	// Writer must complete quickly even though gate is closed
	select {
	case <-writeDone:
		t.Log("all lines written while gate was closed — no stdout blocking")
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: ptp4l stdout write blocked because scanner is gated")
	}

	// Scanner should finish and lineCh should have all lines
	select {
	case <-scannerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: scanner goroutine did not finish")
	}

	received := 0
	for range lineCh {
		received++
	}
	assert.Equal(t, writeCount, received, "scanner should have drained all lines into lineCh")
}

// TestLiveGateIntegration_ReplayThenLive simulates the full two-connection
// scenario: the EventHandler writes replay data through its own connection
// (conn B) while the daemon's socket-writer sends live data through conn A.
// CEP accepts both connections and processes them independently.
func TestLiveGateIntegration_ReplayThenLive(t *testing.T) {
	socketPath := shortTestSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()

	replayLines := []string{
		"ptp4l[0.000]: [ptp4l.0.config] port 1 (ens1f0): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
		"phc2sys[0.001]: [ptp4l.0.config] CLOCK_REALTIME phc offset 0 s2 freq -100 delay 500",
	}
	liveLines := []string{
		testPtp4lOffsetLine,
		"ptp4l[2.000]: [ptp4l.0.config] selected best master clock 001122.fffe.334455",
	}

	// Daemon HTTP server. The handler:
	// 1. Writes replay data through a NEW socket connection (EventHandler path)
	// 2. Opens the live gate
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("/emit-logs", func(w http.ResponseWriter, _ *http.Request) {
		// Write replay data through a separate connection (simulates EventHandler)
		replayConn, dialErr := net.DialTimeout("unix", socketPath, 2*time.Second)
		if dialErr != nil {
			t.Logf("replay dial failed (non-fatal): %v", dialErr)
		} else {
			for _, line := range replayLines {
				fmt.Fprintf(replayConn, "%s\n", line)
			}
			replayConn.Close()
		}
		dn.liveGate.Open()
		w.WriteHeader(http.StatusOK)
	})
	httpSrv := &http.Server{Handler: mux}
	go func() { _ = httpSrv.Serve(httpLn) }()
	defer httpSrv.Close()
	triggerURL := "http://" + httpLn.Addr().String() + "/emit-logs"

	// CEP side: accept ALL connections, each runs processMessages.
	type connResult struct {
		label string
		msgs  []string
	}
	results := make(chan connResult, 5)
	var cepWg sync.WaitGroup
	go func() {
		idx := 0
		for {
			fd, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			label := fmt.Sprintf("conn-%d", idx)
			idx++
			cepWg.Add(1)
			go func(c net.Conn, lbl string) {
				defer cepWg.Done()
				defer c.Close()

				client := &http.Client{Timeout: 5 * time.Second}
				resp, trigErr := client.Get(triggerURL)
				if trigErr != nil {
					t.Logf("CEP %s TriggerLogs failed: %v (non-fatal)", lbl, trigErr)
					return
				}
				resp.Body.Close()

				var msgs []string
				s := bufio.NewScanner(c)
				for s.Scan() {
					msgs = append(msgs, s.Text())
				}
				results <- connResult{label: lbl, msgs: msgs}
			}(fd, label)
		}
	}()

	// Daemon socket-writer: connects, waits for gate, sends LIVE_START + data
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c, dialErr := net.DialTimeout("unix", socketPath, 5*time.Second)
		if dialErr != nil {
			t.Errorf("socket-writer dial: %v", dialErr)
			return
		}
		defer c.Close()
		dn.liveGate.Wait(liveGateTimeout)
		fmt.Fprintf(c, "%s\n", liveStartCommand)
		for _, line := range liveLines {
			fmt.Fprintf(c, "%s\n", line)
		}
	}()

	select {
	case <-writerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: socket-writer blocked — possible deadlock")
	}

	// Shut down listener and wait for CEP goroutines to drain.
	listener.Close()
	cepWg.Wait()
	close(results)

	var liveResult, replayResult *connResult
	for r := range results {
		r := r
		hasLiveStart := false
		for _, m := range r.msgs {
			if m == liveStartCommand {
				hasLiveStart = true
				break
			}
		}
		if hasLiveStart {
			liveResult = &r
		} else if len(r.msgs) > 0 {
			replayResult = &r
		}
	}

	if assert.NotNil(t, liveResult, "should have connection with LIVE_START") {
		t.Logf("live %s: %d msgs", liveResult.label, len(liveResult.msgs))
		assert.Equal(t, liveStartCommand, liveResult.msgs[0])
		for _, expected := range liveLines {
			found := false
			for _, m := range liveResult.msgs {
				if m == expected {
					found = true
					break
				}
			}
			assert.True(t, found, "live connection missing: %s", expected)
		}
	}

	if replayResult != nil {
		t.Logf("replay %s: %d msgs", replayResult.label, len(replayResult.msgs))
		for _, expected := range replayLines {
			found := false
			for _, m := range replayResult.msgs {
				if m == expected {
					found = true
					break
				}
			}
			assert.True(t, found, "replay connection missing: %s", expected)
		}
	}
}

// TestLiveGateIntegration_OrderingPreventsCorruption verifies the core
// OCPBUGS-85092 fix: the live gate ensures stale replay data (SLAVE→FAULTY)
// is fully delivered BEFORE CMD LIVE_START and live data. CEP processes replay
// first on a separate connection, then receives LIVE_START + live LOCKED offsets
// on the daemon connection — so the live state is always the final state.
//
// Without the gate, the socket-writer would send live data immediately, and
// the /emit-logs handler would write stale replay AFTER — permanently poisoning
// the metrics.
func TestLiveGateIntegration_OrderingPreventsCorruption(t *testing.T) {
	socketPath := shortTestSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()

	replayLines := []string{
		"ptp4l[0.000]: [ptp4l.0.config] port 1 (ens1f0): SLAVE to FAULTY on FAULT_DETECTED (FT_UNSPECIFIED)",
	}
	liveLines := []string{
		"ptp4l[10.000]: [ptp4l.0.config] master offset 3 s2 freq -998 path delay 99",
		"ptp4l[11.000]: [ptp4l.0.config] master offset 2 s2 freq -997 path delay 100",
	}

	type taggedMsg struct {
		connIdx int
		msg     string
	}
	allMsgs := make(chan taggedMsg, 100)
	replayReadDone := make(chan struct{})
	var accepted int32
	var cepWg sync.WaitGroup

	go func() {
		for {
			fd, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			ci := int(atomic.AddInt32(&accepted, 1) - 1)
			cepWg.Add(1)
			go func(c net.Conn, idx int) {
				defer cepWg.Done()
				defer c.Close()
				s := bufio.NewScanner(c)
				for s.Scan() {
					allMsgs <- taggedMsg{connIdx: idx, msg: s.Text()}
				}
				if idx == 1 {
					close(replayReadDone)
				}
			}(fd, ci)
		}
	}()

	// Daemon socket-writer (conn-0): connects first, blocks on gate
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c, dialErr := net.DialTimeout("unix", socketPath, 5*time.Second)
		if dialErr != nil {
			t.Errorf("socket-writer dial: %v", dialErr)
			return
		}
		defer c.Close()
		dn.liveGate.Wait(liveGateTimeout)
		fmt.Fprintf(c, "%s\n", liveStartCommand)
		for _, line := range liveLines {
			fmt.Fprintf(c, "%s\n", line)
		}
	}()

	// /emit-logs handler (conn-1): writes replay data, waits for receipt, opens gate
	emitDone := make(chan struct{})
	go func() {
		defer close(emitDone)
		// Wait until live writer's connection is accepted (conn-0)
		for atomic.LoadInt32(&accepted) < 1 {
			time.Sleep(time.Millisecond)
		}

		replayConn, dialErr := net.DialTimeout("unix", socketPath, 2*time.Second)
		if dialErr != nil {
			t.Errorf("replay dial failed: %v", dialErr)
			dn.liveGate.Open()
			return
		}
		for _, line := range replayLines {
			fmt.Fprintf(replayConn, "%s\n", line)
		}
		replayConn.Close()

		// Wait for the replay reader goroutine to fully consume the data.
		// In production, kernel socket buffering makes this near-instantaneous.
		<-replayReadDone
		dn.liveGate.Open()
	}()

	select {
	case <-writerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: socket-writer blocked")
	}
	<-emitDone

	for atomic.LoadInt32(&accepted) < 2 {
		time.Sleep(time.Millisecond)
	}
	listener.Close()
	cepWg.Wait()
	close(allMsgs)

	var ordered []taggedMsg
	for m := range allMsgs {
		ordered = append(ordered, m)
	}

	replayIdx := -1
	liveStartIdx := -1
	firstLiveDataIdx := -1
	for i, m := range ordered {
		if strings.Contains(m.msg, "SLAVE to FAULTY") && replayIdx == -1 {
			replayIdx = i
		}
		if m.msg == liveStartCommand && liveStartIdx == -1 {
			liveStartIdx = i
		}
		if strings.Contains(m.msg, "master offset") && firstLiveDataIdx == -1 {
			firstLiveDataIdx = i
		}
	}

	t.Logf("message order: replay@%d, LIVE_START@%d, first_live@%d (total %d msgs)",
		replayIdx, liveStartIdx, firstLiveDataIdx, len(ordered))
	for i, m := range ordered {
		t.Logf("  [%d] conn-%d: %s", i, m.connIdx, m.msg)
	}

	assert.Greater(t, replayIdx, -1, "replay line should be present")
	assert.Greater(t, liveStartIdx, -1, "LIVE_START should be present")
	assert.Greater(t, firstLiveDataIdx, -1, "live data should be present")

	if replayIdx >= 0 && liveStartIdx >= 0 {
		assert.Less(t, replayIdx, liveStartIdx,
			"CRITICAL: stale replay must arrive BEFORE CMD LIVE_START (gate guarantee)")
	}
	if liveStartIdx >= 0 && firstLiveDataIdx >= 0 {
		assert.Less(t, liveStartIdx, firstLiveDataIdx,
			"live data must arrive AFTER CMD LIVE_START")
	}
}

// TestLiveGateIntegration_TriggerLogsFailureRecovery verifies that when
// TriggerLogs fails (daemon HTTP not ready), CEP closes the connection,
// and the daemon's socket-writer can recover once the server comes up.
func TestLiveGateIntegration_TriggerLogsFailureRecovery(t *testing.T) {
	socketPath := shortTestSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()

	// Reserve an address but DON'T serve yet — TriggerLogs will fail.
	httpLn, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NoError(t, err)
	httpAddr := httpLn.Addr().String()
	httpLn.Close() // intentionally closed

	triggerURL := "http://" + httpAddr + "/emit-logs"
	firstFail := make(chan struct{}, 1)
	success := make(chan []string, 1)

	// CEP accept loop
	go func() {
		for {
			fd, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				client := &http.Client{Timeout: 1 * time.Second}
				resp, trigErr := client.Get(triggerURL)
				if trigErr != nil {
					select {
					case firstFail <- struct{}{}:
					default:
					}
					return // CEP closes conn on failure (matches real behavior)
				}
				resp.Body.Close()
				var msgs []string
				s := bufio.NewScanner(c)
				for s.Scan() {
					msgs = append(msgs, s.Text())
				}
				success <- msgs
			}(fd)
		}
	}()

	// Daemon socket-writer with retry loop (mirrors real code's goto connect).
	// Uses a short gate timeout so the test doesn't block for 60s.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for attempt := 0; attempt < 30; attempt++ {
			c, dialErr := net.DialTimeout("unix", socketPath, 2*time.Second)
			if dialErr != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			// Short gate timeout: if CEP closed the connection (TriggerLogs
			// failed), the gate won't open for this attempt. Rather than
			// blocking for 60s, use a short select.
			dn.liveGate.Wait(2 * time.Second)
			_, wErr := fmt.Fprintf(c, "%s\nlive data\n", liveStartCommand)
			c.Close()
			if wErr == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Error("socket-writer exhausted retries")
	}()

	// Wait for first failure
	select {
	case <-firstFail:
		t.Log("first TriggerLogs failed as expected")
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for first TriggerLogs failure")
	}

	// Start the HTTP server
	newLn, err := net.Listen("tcp", httpAddr)
	if err != nil {
		t.Skipf("cannot rebind %s: %v", httpAddr, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/emit-logs", func(w http.ResponseWriter, _ *http.Request) {
		dn.liveGate.Open()
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(newLn) }()
	defer srv.Close()

	select {
	case <-writerDone:
		t.Log("socket-writer recovered after HTTP server started")
	case <-time.After(15 * time.Second):
		t.Fatal("timeout: socket-writer did not recover")
	}

	listener.Close()
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

// --- /emit-logs handler unit tests ---

func TestEmitLogsHandler_NotReady_OpensGateAndReturns204(t *testing.T) {
	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()

	tracker := &ReadyTracker{
		config: false, // not ready
		processManager: &ProcessManager{
			daemon: dn,
		},
	}

	handler := metricHandler{tracker: tracker}
	req := httptest.NewRequest("GET", "/emit-logs", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code, "not-ready should return 204")

	dn.liveGate.lock.RLock()
	defer dn.liveGate.lock.RUnlock()
	select {
	case <-dn.liveGate.c:
		// expected: gate is closed (open)
	default:
		t.Fatal("liveGate should be open after not-ready /emit-logs")
	}
}

func TestEmitLogsHandler_Ready_EmitsReplayThenOpensGate(t *testing.T) {
	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()

	// Provide a real socket so EmitPortRoleLogs can connect without retry delays.
	socketPath := shortTestSocketPath(t)
	ln, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer ln.Close()
	go func() {
		for {
			c, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			c.Close()
		}
	}()

	// A "ready" tracker requires config=true and at least one process
	// that is not stopped and has collected metrics.
	tracker := &ReadyTracker{
		config: true,
		processManager: &ProcessManager{
			process: []*ptpProcess{
				{
					name:                ptp4lProcessName,
					hasCollectedMetrics: true,
					stopped:             false,
				},
			},
			ptpEventHandler: event.Init("test-node", false, socketPath, nil, nil, nil, nil, nil),
			daemon:          dn,
		},
	}

	handler := metricHandler{tracker: tracker}
	req := httptest.NewRequest("GET", "/emit-logs", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "ready should return 200")

	// liveGate must be open after synchronous replay completes
	dn.liveGate.lock.RLock()
	defer dn.liveGate.lock.RUnlock()
	select {
	case <-dn.liveGate.c:
		// expected: gate is open
	default:
		t.Fatal("liveGate should be open after ready /emit-logs completes synchronous replay")
	}
}

func TestLiveGate_TimeoutReturnsTrue(t *testing.T) {
	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()
	// Gate is never opened — should timeout and return true.

	result := make(chan bool, 1)
	go func() {
		dn.liveGate.Wait(50 * time.Millisecond)
		result <- true
	}()

	// Should NOT return within 50ms (gate is still closed, timeout hasn't fired)
	select {
	case <-result:
		t.Fatal("waitForLiveGate returned before timeout")
	case <-time.After(50 * time.Millisecond):
	}

	// Should return true after the 100ms timeout
	select {
	case r := <-result:
		assert.True(t, r, "timeout should return true (proceed without guarantee)")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("waitForLiveGate did not return after timeout")
	}
}

// TestSocketWriter_Drain verifies that lines buffered in lineCh while
// the gate was closed are drained (discarded) by the socket-writer and
// NOT forwarded to the socket. Only lines produced after the drain
// should appear on the CEP side.
func TestSocketWriter_Drain(t *testing.T) {
	socketPath := shortTestSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()

	lineCh := make(chan string, 256)

	staleLines := []string{
		"ptp4l[0.001]: [ptp4l.0.config] master offset 999 s2 freq -5000 path delay 200",
		"ptp4l[0.002]: [ptp4l.0.config] master offset 998 s2 freq -4999 path delay 201",
		"ptp4l[0.003]: [ptp4l.0.config] master offset 997 s2 freq -4998 path delay 202",
	}
	liveLines := []string{
		"ptp4l[10.000]: [ptp4l.0.config] master offset 3 s2 freq -998 path delay 99",
		"ptp4l[11.000]: [ptp4l.0.config] master offset 2 s2 freq -997 path delay 100",
	}

	// Buffer stale lines BEFORE the gate opens.
	for _, line := range staleLines {
		lineCh <- line
	}

	// CEP side: accept one connection, read all messages.
	cepMsgs := make(chan []string, 1)
	go func() {
		fd, acceptErr := listener.Accept()
		if acceptErr != nil {
			cepMsgs <- nil
			return
		}
		defer fd.Close()
		var msgs []string
		s := bufio.NewScanner(fd)
		for s.Scan() {
			msgs = append(msgs, s.Text())
		}
		cepMsgs <- msgs
	}()

	// Socket-writer goroutine: mirrors the real daemon code path.
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c, dialErr := net.DialTimeout("unix", socketPath, 5*time.Second)
		if dialErr != nil {
			t.Errorf("socket-writer dial: %v", dialErr)
			return
		}
		defer c.Close()

		dn.liveGate.Wait(liveGateTimeout)
		fmt.Fprintf(c, "%s\n", liveStartCommand)

		// Drain stale lines (same logic as daemon.go drainLoop)
		drained := 0
	drainLoop:
		for {
			select {
			case _, ok := <-lineCh:
				if !ok {
					return
				}
				drained++
			default:
				break drainLoop
			}
		}
		t.Logf("drained %d stale lines", drained)

		// Forward live lines
		for line := range lineCh {
			fmt.Fprintf(c, "%s\n", line)
		}
	}()

	// Open the gate (stale lines are already buffered).
	dn.liveGate.Open()

	// Feed live lines after a short delay to ensure drain has completed.
	go func() {
		time.Sleep(50 * time.Millisecond)
		for _, line := range liveLines {
			lineCh <- line
		}
		close(lineCh)
	}()

	select {
	case <-writerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: socket-writer did not finish")
	}

	select {
	case msgs := <-cepMsgs:
		if !assert.NotNil(t, msgs, "CEP should have received messages") {
			return
		}
		t.Logf("CEP received %d messages: %v", len(msgs), msgs)

		// LIVE_START should be first
		assert.Equal(t, liveStartCommand, msgs[0], "first message should be CMD LIVE_START")

		// Stale lines must NOT appear
		for _, stale := range staleLines {
			for _, m := range msgs {
				assert.NotEqual(t, stale, m, "stale line should have been drained, not forwarded")
			}
		}

		// Live lines must appear
		for _, live := range liveLines {
			found := false
			for _, m := range msgs {
				if m == live {
					found = true
					break
				}
			}
			assert.True(t, found, "live line missing from CEP: %s", live)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: CEP did not finish reading")
	}
}

// TestSocketWriter_Reconnect verifies that when a socket write fails
// mid-stream, the socket-writer reconnects and delivers subsequent
// lines on the new connection.
func TestSocketWriter_Reconnect(t *testing.T) {
	socketPath := shortTestSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()
	dn.liveGate.Open() // gate is already open for this test

	lineCh := make(chan string, 256)
	doneCh := make(chan struct{}, 1)

	lines := []string{
		testPtp4lOffsetLine,
		"ptp4l[2.000]: [ptp4l.0.config] master offset 3 s2 freq -998 path delay 99",
		"ptp4l[3.000]: [ptp4l.0.config] master offset 1 s2 freq -996 path delay 98",
	}

	// Track messages across all connections
	type connMsgs struct {
		idx  int
		msgs []string
	}
	allResults := make(chan connMsgs, 10)
	var connCount int32

	// CEP accept loop: first connection will be closed after accepting
	// to simulate a write failure; second connection reads normally.
	go func() {
		for {
			fd, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			ci := int(atomic.AddInt32(&connCount, 1))
			if ci == 1 {
				// First connection: close immediately to trigger write failure
				time.Sleep(10 * time.Millisecond)
				fd.Close()
				allResults <- connMsgs{idx: ci, msgs: nil}
				continue
			}
			go func(c net.Conn, idx int) {
				defer c.Close()
				var msgs []string
				s := bufio.NewScanner(c)
				for s.Scan() {
					msgs = append(msgs, s.Text())
				}
				allResults <- connMsgs{idx: idx, msgs: msgs}
			}(fd, ci)
		}
	}()

	// Socket-writer with reconnect logic (mirrors daemon.go)
	go func() {
	connect:
		select {
		case <-doneCh:
			return
		default:
		}
		c, dialErr := net.DialTimeout("unix", socketPath, 5*time.Second)
		if dialErr != nil {
			time.Sleep(10 * time.Millisecond)
			goto connect
		}
		dn.liveGate.Wait(liveGateTimeout)
		if _, err2 := fmt.Fprintf(c, "%s\n", liveStartCommand); err2 != nil {
			c.Close()
			goto connect
		}
		for line := range lineCh {
			_, err2 := fmt.Fprintf(c, "%s\n", line)
			if err2 != nil {
				c.Close()
				goto connect
			}
		}
		c.Close()
		doneCh <- struct{}{}
	}()

	// Feed lines with a delay so the first write hits the closed connection
	go func() {
		time.Sleep(100 * time.Millisecond)
		for _, line := range lines {
			lineCh <- line
			time.Sleep(10 * time.Millisecond)
		}
		close(lineCh)
	}()

	select {
	case <-doneCh:
		t.Log("socket-writer completed with reconnect")
	case <-time.After(10 * time.Second):
		t.Fatal("timeout: socket-writer did not finish")
	}
	listener.Close()

	// Collect results. The second connection should have received at least some lines.
	var secondConn *connMsgs
	timeout := time.After(2 * time.Second)
	for secondConn == nil {
		select {
		case r := <-allResults:
			if r.idx == 2 {
				secondConn = &r
			}
		case <-timeout:
			t.Fatal("timeout: did not receive second connection results")
		}
	}

	assert.NotNil(t, secondConn, "second connection should exist after reconnect")
	assert.Greater(t, len(secondConn.msgs), 0, "second connection should have received messages")
	t.Logf("second connection received %d messages: %v", len(secondConn.msgs), secondConn.msgs)

	// At least the LIVE_START marker should be present on reconnect
	hasLiveStart := false
	for _, m := range secondConn.msgs {
		if m == liveStartCommand {
			hasLiveStart = true
			break
		}
	}
	assert.True(t, hasLiveStart, "reconnected connection should have LIVE_START")
}

// TestSocketWriter_SuffixStrip verifies that removeMessageSuffix strips
// severity tags and template braces from lines before they are forwarded
// through the socket.
func TestSocketWriter_SuffixStrip(t *testing.T) {
	socketPath := shortTestSocketPath(t)
	listener, err := net.Listen("unix", socketPath)
	assert.NoError(t, err)
	defer listener.Close()

	dn := &Daemon{
		liveGate: &liveGate{},
	}
	dn.liveGate.Reset()
	dn.liveGate.Open()

	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "ptp4l[2464681.628]: [phc2sys.1.config:7] master offset -4 s2 freq -26835 path delay 525",
			expected: "ptp4l[2464681.628]: [phc2sys.1.config] master offset -4 s2 freq -26835 path delay 525",
		},
		{
			input:    "ptp4l[2464681.628]: [phc2sys.1.config:{level}] master offset -4 s2 freq -26835 path delay 525",
			expected: "ptp4l[2464681.628]: [phc2sys.1.config] master offset -4 s2 freq -26835 path delay 525",
		},
		{
			input:    testPtp4lOffsetLine,
			expected: testPtp4lOffsetLine,
		},
	}

	lineCh := make(chan string, 256)

	// CEP side
	cepMsgs := make(chan []string, 1)
	go func() {
		fd, acceptErr := listener.Accept()
		if acceptErr != nil {
			cepMsgs <- nil
			return
		}
		defer fd.Close()
		var msgs []string
		s := bufio.NewScanner(fd)
		for s.Scan() {
			msgs = append(msgs, s.Text())
		}
		cepMsgs <- msgs
	}()

	// Socket-writer that applies removeMessageSuffix (mirrors daemon.go line 1758)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c, dialErr := net.DialTimeout("unix", socketPath, 5*time.Second)
		if dialErr != nil {
			t.Errorf("dial failed: %v", dialErr)
			return
		}
		defer c.Close()

		dn.liveGate.Wait(liveGateTimeout)
		fmt.Fprintf(c, "%s\n", liveStartCommand)
		for output := range lineCh {
			line := removeMessageSuffix(output) + "\n"
			c.Write([]byte(line))
		}
	}()

	// Feed test inputs
	for _, tc := range tests {
		lineCh <- tc.input
	}
	close(lineCh)

	select {
	case <-writerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: socket-writer did not finish")
	}

	select {
	case msgs := <-cepMsgs:
		if !assert.NotNil(t, msgs, "CEP should have received messages") {
			return
		}
		// First message is LIVE_START
		assert.Equal(t, liveStartCommand, msgs[0])
		// Remaining messages should be the suffix-stripped versions
		for i, tc := range tests {
			msgIdx := i + 1 // skip LIVE_START
			if assert.Greater(t, len(msgs), msgIdx, "should have enough messages") {
				assert.Equal(t, tc.expected, msgs[msgIdx],
					"line %d: removeMessageSuffix should strip suffix before forwarding", i)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: CEP did not finish reading")
	}
}
