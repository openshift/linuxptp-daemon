package hardwareconfig

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

// Test constants to avoid goconst warnings
const (
	testProtoVersion    = "29.20"
	testProtoVersion2   = "29.25"
	testAntVoltEnable   = "CFG-HW-ANT_CFG_VOLTCTRL,1"
	testProfileName     = "tgm_grandmaster" // stored name with clock-type prefix
	testHWConfigName    = "grandmaster"     // plain relatedPtpProfileName
	testSourcePTP       = "PTP"
	testSourceGNSS      = "GNSS"
	testIfaceEno8703    = "eno8703"
	testIfaceEns7f0     = "ens7f0"
	testSubsystemLeader = "leader"
	testDevPtp0         = "/dev/ptp0"
	testClockTypeTGM    = ClockTypeTGM
	testSurveyInArgs    = "SURVEYIN,600,50000"
	testMonHW           = "MON-HW"
	testCfgMsg          = "CFG-MSG,1,38,248"
	testACM0            = "/dev/ttyACM0"
)

// --- Mock helpers ---

// mockRunner implements ublox.Runner for testing, recording all commands.
type mockRunner struct {
	commands      []ublox.Command
	defaultOutput string
	defaultErr    error
}

func (m *mockRunner) Run(cmd ublox.Command) (string, error) {
	m.commands = append(m.commands, cmd)
	return m.defaultOutput, m.defaultErr
}

func (m *mockRunner) Poll(cmd ublox.Command, responseType ublox.MessageType) (ublox.Message, error) {
	output, err := m.Run(cmd)
	return ublox.Message{Type: responseType, Raw: []string{output}}, err
}

func (m *mockRunner) RunAll(cmds ublox.CommandList, withSave bool) []string {
	var results []string
	for _, cmd := range cmds {
		output, err := m.Run(cmd)
		if cmd.ReportOutput {
			if err != nil {
				results = append(results, err.Error())
			} else {
				results = append(results, output)
			}
		}
	}
	if withSave {
		m.Run(ublox.SaveCommand) //nolint:errcheck
	}
	return results
}

func (m *mockRunner) Save() error {
	_, err := m.Run(ublox.SaveCommand)
	return err
}

func (m *mockRunner) containsCommand(substrs ...string) bool {
	for _, cmd := range m.commands {
		joined := strings.Join(cmd.Args, " ")
		allFound := true
		for _, s := range substrs {
			if !strings.Contains(joined, s) {
				allFound = false
				break
			}
		}
		if allFound {
			return true
		}
	}
	return false
}

func setupRunnerMock() (*mockRunner, func()) {
	orig := ublox.NewCommandRunnerFn
	mock := &mockRunner{defaultOutput: "OK"}
	ublox.NewCommandRunnerFn = func() (ublox.Runner, error) {
		return mock, nil
	}
	return mock, func() { ublox.NewCommandRunnerFn = orig }
}

// Reuses mockDirEntry from clockchain_resolution_test.go (pointer receiver, same package)

func setupReadDirMock(entries map[string][]os.DirEntry, errs map[string]error) func() {
	orig := ublox.ReadDir
	ublox.ReadDir = func(name string) ([]os.DirEntry, error) {
		if errs != nil {
			if err, ok := errs[name]; ok {
				return nil, err
			}
		}
		if entries != nil {
			if e, ok := entries[name]; ok {
				return e, nil
			}
		}
		return nil, errors.New("not found")
	}
	return func() { ublox.ReadDir = orig }
}

// --- Tests ---

func TestAntennaVoltageCommand(t *testing.T) {
	t.Run("enabled", func(t *testing.T) {
		cmd := antennaVoltageCommand(true)
		assert.Equal(t, []string{"-z", testAntVoltEnable}, cmd.Args)
		assert.False(t, cmd.ReportOutput)
	})

	t.Run("disabled", func(t *testing.T) {
		cmd := antennaVoltageCommand(false)
		assert.Equal(t, []string{"-z", "CFG-HW-ANT_CFG_VOLTCTRL,0"}, cmd.Args)
	})
}

// extractConstellationFlags parses the batched args of a constellation command,
// returning the names following -e and -d flags separately.
func extractConstellationFlags(cmd ublox.Command) (enabled, disabled []string) {
	for i := 0; i < len(cmd.Args)-1; i += 2 {
		switch cmd.Args[i] {
		case "-e":
			enabled = append(enabled, cmd.Args[i+1])
		case "-d":
			disabled = append(disabled, cmd.Args[i+1])
		}
	}
	return
}

func TestConstellationCommand(t *testing.T) {
	t.Run("GPS only", func(t *testing.T) {
		cmd := constellationCommand([]ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS})
		enabled, disabled := extractConstellationFlags(cmd)

		assert.Equal(t, []string{ubxtoolConstellationName[ptpv2alpha1.ConstellationGPS]}, enabled)
		assert.ElementsMatch(t, []string{
			ubxtoolConstellationName[ptpv2alpha1.ConstellationGalileo],
			ubxtoolConstellationName[ptpv2alpha1.ConstellationGLONASS],
			ubxtoolConstellationName[ptpv2alpha1.ConstellationBeiDou],
			ubxtoolConstellationName[ptpv2alpha1.ConstellationSBAS],
		}, disabled)
	})

	t.Run("multiple constellations", func(t *testing.T) {
		cmd := constellationCommand([]ptpv2alpha1.ConstellationID{
			ptpv2alpha1.ConstellationGPS,
			ptpv2alpha1.ConstellationGalileo,
			ptpv2alpha1.ConstellationBeiDou,
		})
		enabled, disabled := extractConstellationFlags(cmd)

		assert.ElementsMatch(t, []string{
			ubxtoolConstellationName[ptpv2alpha1.ConstellationGPS],
			ubxtoolConstellationName[ptpv2alpha1.ConstellationGalileo],
			ubxtoolConstellationName[ptpv2alpha1.ConstellationBeiDou],
		}, enabled)
		assert.ElementsMatch(t, []string{
			ubxtoolConstellationName[ptpv2alpha1.ConstellationGLONASS],
			ubxtoolConstellationName[ptpv2alpha1.ConstellationSBAS],
		}, disabled)
	})

	t.Run("GLONASS maps to GLONASS for ubxtool", func(t *testing.T) {
		cmd := constellationCommand([]ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGLONASS})
		enabled, _ := extractConstellationFlags(cmd)

		assert.Contains(t, enabled, ubxtoolConstellationName[ptpv2alpha1.ConstellationGLONASS],
			"GLONASS should map to GLONASS for ubxtool")
	})

	t.Run("empty list disables all", func(t *testing.T) {
		cmd := constellationCommand(nil)
		enabled, disabled := extractConstellationFlags(cmd)

		assert.Empty(t, enabled, "no constellations should be enabled")
		assert.Equal(t, len(allConstellationIDs), len(disabled), "all constellations should be disabled")
	})
}

func TestSurveyInCommand(t *testing.T) {
	t.Run("standard parameters", func(t *testing.T) {
		cmd := surveyInCommand(ptpv2alpha1.GNSSSurveyParameters{
			ObservationTime: 600,
			Accuracy:        5,
		})

		assert.Equal(t, []string{
			"-t", "-w", "5", "-v", "1",
			"-e", testSurveyInArgs,
		}, cmd.Args)
		assert.True(t, cmd.ReportOutput)
	})

	t.Run("1 meter accuracy", func(t *testing.T) {
		cmd := surveyInCommand(ptpv2alpha1.GNSSSurveyParameters{
			ObservationTime: 300,
			Accuracy:        1,
		})
		assert.Contains(t, cmd.Args, "SURVEYIN,300,10000")
	})
}

func TestBuildGNSSInitCommands(t *testing.T) {
	config := &ptpv2alpha1.GNSSConfig{
		Init: ptpv2alpha1.GNSSInit{
			AntennaVoltage: true,
			Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
			SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
				ObservationTime: 600,
				Accuracy:        5,
			},
			ExtraCommands: []ptpv2alpha1.UBLXCommand{
				{Args: []string{"-p", testMonHW}, Record: true},
				{Args: []string{"-p", testCfgMsg}, Record: true},
			},
		},
	}

	cmds := buildGNSSInitCommands(config)

	// 1 antenna + 1 constellation (batched) + 1 survey + 2 extras = 5
	assert.Equal(t, 5, len(cmds))

	// First: antenna voltage
	assert.Equal(t, []string{"-z", testAntVoltEnable}, cmds[0].Args)

	// Second: batched constellation command
	enabledConst, disabledConst := extractConstellationFlags(cmds[1])
	assert.Equal(t, []string{ubxtoolConstellationName[ptpv2alpha1.ConstellationGPS]}, enabledConst)
	assert.ElementsMatch(t, []string{
		ubxtoolConstellationName[ptpv2alpha1.ConstellationGalileo],
		ubxtoolConstellationName[ptpv2alpha1.ConstellationGLONASS],
		ubxtoolConstellationName[ptpv2alpha1.ConstellationBeiDou],
		ubxtoolConstellationName[ptpv2alpha1.ConstellationSBAS],
	}, disabledConst)

	// Survey-in
	assert.Contains(t, cmds[2].Args, testSurveyInArgs)
	assert.True(t, cmds[2].ReportOutput)

	// Extra commands
	assert.Equal(t, []string{"-p", testMonHW}, cmds[3].Args)
	assert.True(t, cmds[3].ReportOutput)
	assert.Equal(t, []string{"-p", testCfgMsg}, cmds[4].Args)
	assert.True(t, cmds[4].ReportOutput)
}

func TestBuildGNSSInitCommands_NoSurvey(t *testing.T) {
	config := &ptpv2alpha1.GNSSConfig{
		Init: ptpv2alpha1.GNSSInit{
			AntennaVoltage: true,
			Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
			SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
				ObservationTime: 0,
				Accuracy:        5,
			},
		},
	}

	cmds := buildGNSSInitCommands(config)

	// 1 antenna + 1 constellation = 2 (no survey-in)
	assert.Equal(t, 2, len(cmds))

	// Verify no SURVEYIN command
	for _, cmd := range cmds {
		for _, arg := range cmd.Args {
			assert.NotContains(t, arg, "SURVEYIN", "should not contain SURVEYIN when ObservationTime is 0")
		}
	}
}

// TestBuildGNSSInitCommands_MatchesOldPluginPattern verifies that the commands
// from buildGNSSInitCommands match what cnfdg60-tgm.yaml expressed via ublxCmds.
func TestBuildGNSSInitCommands_MatchesOldPluginPattern(t *testing.T) {
	mock, restore := setupRunnerMock()
	defer restore()

	config := &ptpv2alpha1.GNSSConfig{
		Init: ptpv2alpha1.GNSSInit{
			AntennaVoltage: true,
			Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
			SurveyIn: ptpv2alpha1.GNSSSurveyParameters{
				ObservationTime: 600,
				Accuracy:        5,
			},
			ExtraCommands: []ptpv2alpha1.UBLXCommand{
				{Args: []string{"-p", testMonHW}, Record: true},
				{Args: []string{"-p", testCfgMsg}, Record: true},
			},
		},
	}

	cmds := buildGNSSInitCommands(config)
	_ = cmds.RunAll(true) // uses mock runner

	// Verify the old-style commands are all present
	expectedPatterns := []string{
		testAntVoltEnable, // antenna voltage on
		"-e GPS",          // enable GPS
		"-d GALILEO",      // disable Galileo
		"-d GLONASS",      // disable GLONASS
		"-d BEIDOU",       // disable BeiDou
		"-d SBAS",         // disable SBAS
		testSurveyInArgs,  // survey-in 600s, 5m accuracy
		testMonHW,         // extra: hardware status
		testCfgMsg,        // extra: message rate
		"SAVE",            // save (always last)
	}

	for _, pattern := range expectedPatterns {
		assert.True(t, mock.containsCommand(pattern),
			"expected command containing %q", pattern)
	}
}

func TestFindGNSSDevice(t *testing.T) {
	t.Run("nil matcher returns empty", func(t *testing.T) {
		device, err := FindGNSSDevice(nil)
		assert.NoError(t, err)
		assert.Empty(t, device)
	})

	t.Run("ttyDevice returned directly", func(t *testing.T) {
		device, err := FindGNSSDevice(&ptpv2alpha1.GNSSMatcher{
			TTYDevice: testACM0,
		})
		assert.NoError(t, err)
		assert.Equal(t, testACM0, device)
	})

	t.Run("ethernetInterface resolves via sysfs", func(t *testing.T) {
		restoreDir := setupReadDirMock(
			map[string][]os.DirEntry{
				"/sys/class/net/eno8703/device/gnss": {&mockDirEntry{name: "gnss0"}},
			},
			nil,
		)
		defer restoreDir()

		device, err := FindGNSSDevice(&ptpv2alpha1.GNSSMatcher{
			EthernetInterface: testIfaceEno8703,
		})
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("ethernetInterface with no gnss device", func(t *testing.T) {
		restoreDir := setupReadDirMock(
			nil,
			map[string]error{
				"/sys/class/net/eno8703/device/gnss": errors.New("no such directory"),
			},
		)
		defer restoreDir()

		_, err := FindGNSSDevice(&ptpv2alpha1.GNSSMatcher{
			EthernetInterface: testIfaceEno8703,
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no GNSS device found")
	})

	t.Run("empty matcher returns error", func(t *testing.T) {
		_, err := FindGNSSDevice(&ptpv2alpha1.GNSSMatcher{})
		assert.Error(t, err)
	})
}

// --- HardwareConfigManager integration tests ---

func testProfile(name string) *ptpv1.PtpProfile {
	return &ptpv1.PtpProfile{Name: &name}
}

func makeTestHCM(configs ...ptpv2alpha1.HardwareConfig) *HardwareConfigManager {
	hcm := &HardwareConfigManager{
		hardwareConfigs: make([]enrichedHardwareConfig, len(configs)),
		hwDefaultsCache: make(map[string]*HardwareDefaults),
		clockIDCache:    make(map[string]uint64),
	}
	for i, c := range configs {
		hcm.hardwareConfigs[i] = enrichedHardwareConfig{HardwareConfig: c}
	}
	return hcm
}

func TestFindGNSSSource(t *testing.T) {
	gnssConfig := &ptpv2alpha1.GNSSConfig{
		Init: ptpv2alpha1.GNSSInit{
			AntennaVoltage: true,
			Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
			SurveyIn:       ptpv2alpha1.GNSSSurveyParameters{ObservationTime: 600, Accuracy: 5},
		},
		Match: &ptpv2alpha1.GNSSMatcher{TTYDevice: testACM0},
	}

	hwConfig := ptpv2alpha1.HardwareConfig{
		Spec: ptpv2alpha1.HardwareConfigSpec{
			RelatedPtpProfileName: testHWConfigName,
			Profile: ptpv2alpha1.HardwareProfile{
				ClockChain: &ptpv2alpha1.ClockChain{
					Behavior: &ptpv2alpha1.Behavior{
						Sources: []ptpv2alpha1.SourceConfig{
							{Name: testSourcePTP, SourceType: ptpv2alpha1.SourceTypePTP},
							{Name: testSourceGNSS, SourceType: ptpv2alpha1.SourceTypeGNSS, GNSSConfig: gnssConfig},
						},
					},
				},
			},
		},
	}

	t.Run("finds GNSS source for matching profile", func(t *testing.T) {
		hcm := makeTestHCM(hwConfig)
		source, config := hcm.findGNSSSource(testProfile(testProfileName))
		assert.NotNil(t, source)
		assert.NotNil(t, config)
		assert.Equal(t, testSourceGNSS, source.Name)
		assert.True(t, config.Init.AntennaVoltage)
	})

	t.Run("returns nil for non-matching profile", func(t *testing.T) {
		hcm := makeTestHCM(hwConfig)
		source, config := hcm.findGNSSSource(testProfile("other-profile"))
		assert.Nil(t, source)
		assert.Nil(t, config)
	})

	t.Run("returns nil when no GNSS source", func(t *testing.T) {
		noGNSS := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
				Profile: ptpv2alpha1.HardwareProfile{
					ClockChain: &ptpv2alpha1.ClockChain{
						Behavior: &ptpv2alpha1.Behavior{
							Sources: []ptpv2alpha1.SourceConfig{
								{Name: testSourcePTP, SourceType: ptpv2alpha1.SourceTypePTP},
							},
						},
					},
				},
			},
		}
		hcm := makeTestHCM(noGNSS)
		source, config := hcm.findGNSSSource(testProfile(testProfileName))
		assert.Nil(t, source)
		assert.Nil(t, config)
	})

	t.Run("returns nil when no behavior", func(t *testing.T) {
		noBehavior := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
			},
		}
		hcm := makeTestHCM(noBehavior)
		source, config := hcm.findGNSSSource(testProfile(testProfileName))
		assert.Nil(t, source)
		assert.Nil(t, config)
	})
}

func TestGetGNSSSerialPort(t *testing.T) {
	t.Run("returns ttyDevice from matcher", func(t *testing.T) {
		hwConfig := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
				Profile: ptpv2alpha1.HardwareProfile{
					ClockChain: &ptpv2alpha1.ClockChain{
						Behavior: &ptpv2alpha1.Behavior{
							Sources: []ptpv2alpha1.SourceConfig{
								{
									Name:       testSourceGNSS,
									SourceType: ptpv2alpha1.SourceTypeGNSS,
									GNSSConfig: &ptpv2alpha1.GNSSConfig{
										Init:  ptpv2alpha1.GNSSInit{},
										Match: &ptpv2alpha1.GNSSMatcher{TTYDevice: testACM0},
									},
								},
							},
						},
					},
				},
			},
		}
		hcm := makeTestHCM(hwConfig)
		port, err := hcm.GetGNSSSerialPort(testProfile(testProfileName))
		assert.NoError(t, err)
		assert.Equal(t, testACM0, port)
	})

	t.Run("returns empty when no GNSS source", func(t *testing.T) {
		hwConfig := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
				Profile: ptpv2alpha1.HardwareProfile{
					ClockChain: &ptpv2alpha1.ClockChain{
						Behavior: &ptpv2alpha1.Behavior{
							Sources: []ptpv2alpha1.SourceConfig{
								{Name: testSourcePTP, SourceType: ptpv2alpha1.SourceTypePTP},
							},
						},
					},
				},
			},
		}
		hcm := makeTestHCM(hwConfig)
		port, err := hcm.GetGNSSSerialPort(testProfile(testProfileName))
		assert.NoError(t, err)
		assert.Empty(t, port)
	})

	t.Run("resolves ethernetInterface via sysfs", func(t *testing.T) {
		restoreDir := setupReadDirMock(
			map[string][]os.DirEntry{
				"/sys/class/net/eno8703/device/gnss": {&mockDirEntry{name: "gnss0"}},
			}, nil,
		)
		defer restoreDir()

		hwConfig := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
				Profile: ptpv2alpha1.HardwareProfile{
					ClockChain: &ptpv2alpha1.ClockChain{
						Behavior: &ptpv2alpha1.Behavior{
							Sources: []ptpv2alpha1.SourceConfig{
								{
									Name:       testSourceGNSS,
									SourceType: ptpv2alpha1.SourceTypeGNSS,
									GNSSConfig: &ptpv2alpha1.GNSSConfig{
										Init:  ptpv2alpha1.GNSSInit{},
										Match: &ptpv2alpha1.GNSSMatcher{EthernetInterface: testIfaceEno8703},
									},
								},
							},
						},
					},
				},
			},
		}
		hcm := makeTestHCM(hwConfig)
		port, err := hcm.GetGNSSSerialPort(testProfile(testProfileName))
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", port)
	})
}

func TestGetGNSSInitCommands(t *testing.T) {
	t.Run("returns commands for GNSS source", func(t *testing.T) {
		hwConfig := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
				Profile: ptpv2alpha1.HardwareProfile{
					ClockChain: &ptpv2alpha1.ClockChain{
						Behavior: &ptpv2alpha1.Behavior{
							Sources: []ptpv2alpha1.SourceConfig{
								{
									Name:       testSourceGNSS,
									SourceType: ptpv2alpha1.SourceTypeGNSS,
									GNSSConfig: &ptpv2alpha1.GNSSConfig{
										Init: ptpv2alpha1.GNSSInit{
											AntennaVoltage: true,
											Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
											SurveyIn:       ptpv2alpha1.GNSSSurveyParameters{ObservationTime: 600, Accuracy: 5},
										},
									},
								},
							},
						},
					},
				},
			},
		}
		hcm := makeTestHCM(hwConfig)

		cmds := hcm.GetGNSSInitCommands(testProfile(testProfileName))
		// 1 antenna + 1 constellation (batched) + 1 survey = 3
		assert.Equal(t, 3, len(cmds))

		// Verify antenna voltage is first
		assert.Equal(t, []string{"-z", testAntVoltEnable}, cmds[0].Args)

		// Verify survey-in is last with ReportOutput
		assert.True(t, cmds[2].ReportOutput)
		assert.Contains(t, cmds[2].Args, testSurveyInArgs)
	})

	t.Run("returns nil when no GNSS source", func(t *testing.T) {
		hwConfig := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
				Profile: ptpv2alpha1.HardwareProfile{
					ClockChain: &ptpv2alpha1.ClockChain{
						Behavior: &ptpv2alpha1.Behavior{
							Sources: []ptpv2alpha1.SourceConfig{
								{Name: testSourcePTP, SourceType: ptpv2alpha1.SourceTypePTP},
							},
						},
					},
				},
			},
		}
		hcm := makeTestHCM(hwConfig)

		cmds := hcm.GetGNSSInitCommands(testProfile(testProfileName))
		assert.Nil(t, cmds)
	})
}
