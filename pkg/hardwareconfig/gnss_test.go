package hardwareconfig

import (
	"errors"
	"os"
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

func TestFindGNSSDevice(t *testing.T) {
	t.Run("nil matcher returns error", func(t *testing.T) {
		device, err := findGNSSDevice(nil)
		assert.Error(t, err)
		assert.Empty(t, device)
	})

	t.Run("ttyDevice returned directly", func(t *testing.T) {
		device, err := findGNSSDevice(&ptpv2alpha1.GNSSMatcher{
			TTYDevice: testACM0,
		})
		assert.NoError(t, err)
		assert.Equal(t, testACM0, device)
	})

	t.Run("ethernetDevice name resolves via sysfs", func(t *testing.T) {
		restoreDir := setupReadDirMock(
			map[string][]os.DirEntry{
				"/sys/class/net/eno8703/device/gnss": {&mockDirEntry{name: "gnss0"}},
			},
			nil,
		)
		defer restoreDir()

		device, err := findGNSSDevice(&ptpv2alpha1.GNSSMatcher{
			EthernetDevice: &ptpv2alpha1.EthernetDevice{Name: testIfaceEno8703},
		})
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("ethernetDevice name with no gnss device", func(t *testing.T) {
		restoreDir := setupReadDirMock(
			nil,
			map[string]error{
				"/sys/class/net/eno8703/device/gnss": errors.New("no such directory"),
			},
		)
		defer restoreDir()

		_, err := findGNSSDevice(&ptpv2alpha1.GNSSMatcher{
			EthernetDevice: &ptpv2alpha1.EthernetDevice{Name: testIfaceEno8703},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no GNSS device found")
	})

	t.Run("serialDevice matcher delegates to ACPI device detection", func(t *testing.T) {
		_, err := findGNSSDevice(&ptpv2alpha1.GNSSMatcher{
			SerialDevice: &ptpv2alpha1.SerialDevice{
				ACPI: &ptpv2alpha1.ACPIDevice{HID: "INTC10EE", UID: "00"},
			},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no tty device found for ACPI serial device")
	})

	t.Run("USB matcher delegates to USB device detection", func(t *testing.T) {
		_, err := findGNSSDevice(&ptpv2alpha1.GNSSMatcher{
			USBDevice: &ptpv2alpha1.USBDevice{Vendor: "invalid", Product: "01a9"},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid USB vendor ID")
	})

	t.Run("empty matcher returns error", func(t *testing.T) {
		_, err := findGNSSDevice(&ptpv2alpha1.GNSSMatcher{})
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
		config, hName, sName := hcm.findGNSSSource(testProfile(testProfileName))
		assert.NotNil(t, config)
		assert.Equal(t, hwConfig.Name, hName)
		assert.Equal(t, testSourceGNSS, sName)
		assert.True(t, config.Init.AntennaVoltage)
	})

	t.Run("returns nil for non-matching profile", func(t *testing.T) {
		hcm := makeTestHCM(hwConfig)
		config, hName, sName := hcm.findGNSSSource(testProfile("other-profile"))
		assert.Nil(t, config)
		assert.Empty(t, hName)
		assert.Empty(t, sName)
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
		config, hName, sName := hcm.findGNSSSource(testProfile(testProfileName))
		assert.Nil(t, config)
		assert.Empty(t, hName)
		assert.Empty(t, sName)
	})

	t.Run("returns nil with names when found without GNSSConfig", func(t *testing.T) {
		noGNSS := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
				Profile: ptpv2alpha1.HardwareProfile{
					ClockChain: &ptpv2alpha1.ClockChain{
						Behavior: &ptpv2alpha1.Behavior{
							Sources: []ptpv2alpha1.SourceConfig{
								{Name: testSourcePTP, SourceType: ptpv2alpha1.SourceTypePTP},
								{Name: testSourceGNSS, SourceType: ptpv2alpha1.SourceTypeGNSS, GNSSConfig: nil},
							},
						},
					},
				},
			},
		}
		hcm := makeTestHCM(noGNSS)
		config, hName, sName := hcm.findGNSSSource(testProfile(testProfileName))
		assert.Nil(t, config)
		assert.Equal(t, hwConfig.Name, hName)
		assert.Equal(t, testSourceGNSS, sName)
	})

	t.Run("returns nil when no behavior", func(t *testing.T) {
		noBehavior := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
			},
		}
		hcm := makeTestHCM(noBehavior)
		config, hName, sName := hcm.findGNSSSource(testProfile(testProfileName))
		assert.Nil(t, config)
		assert.Empty(t, hName)
		assert.Empty(t, sName)
	})
}

func TestGetGNSSInitConfigGPSIncludesQZSS(t *testing.T) {
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
										Constellations: []ptpv2alpha1.ConstellationID{ptpv2alpha1.ConstellationGPS},
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

	config := hcm.GetGNSSInitConfig(testProfile(testProfileName))

	if assert.NotNil(t, config) {
		assert.Equal(t, []ublox.Constellation{ublox.ConstellationGPS, ublox.ConstellationQZSS}, config.Constellations)
	}
}

func TestHasExplicitGNSSMatcher(t *testing.T) {
	tests := []struct {
		name       string
		sourceType ptpv2alpha1.SourceTypeID
		gnssConfig *ptpv2alpha1.GNSSConfig
		want       bool
	}{
		{
			name:       "no GNSS source",
			sourceType: ptpv2alpha1.SourceTypePTP,
		},
		{
			name:       "GNSS source without GNSS config",
			sourceType: ptpv2alpha1.SourceTypeGNSS,
		},
		{
			name:       "GNSS config without matcher",
			sourceType: ptpv2alpha1.SourceTypeGNSS,
			gnssConfig: &ptpv2alpha1.GNSSConfig{},
		},
		{
			name:       "configured matcher",
			sourceType: ptpv2alpha1.SourceTypeGNSS,
			gnssConfig: &ptpv2alpha1.GNSSConfig{Match: &ptpv2alpha1.GNSSMatcher{TTYDevice: testACM0}},
			want:       true,
		},
		{
			name:       "empty matcher is still explicit configuration",
			sourceType: ptpv2alpha1.SourceTypeGNSS,
			gnssConfig: &ptpv2alpha1.GNSSConfig{Match: &ptpv2alpha1.GNSSMatcher{}},
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hwConfig := ptpv2alpha1.HardwareConfig{
				Spec: ptpv2alpha1.HardwareConfigSpec{
					RelatedPtpProfileName: testHWConfigName,
					Profile: ptpv2alpha1.HardwareProfile{
						ClockChain: &ptpv2alpha1.ClockChain{
							Behavior: &ptpv2alpha1.Behavior{Sources: []ptpv2alpha1.SourceConfig{{
								Name:       testSourceGNSS,
								SourceType: tt.sourceType,
								GNSSConfig: tt.gnssConfig,
							}}},
						},
					},
				},
			}
			hcm := makeTestHCM(hwConfig)
			assert.Equal(t, tt.want, hcm.HasExplicitGNSSMatcher(testProfile(testProfileName)))
		})
	}
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
		configs := hcm.CloneHardwareConfigs()
		assert.Equal(t, []ptpv2alpha1.SourceStatus{{
			Name: testSourceGNSS,
			Gnss: &ptpv2alpha1.GNSSStatus{
				TTYDevice:   testACM0,
				MatchResult: "Matched GNSS tty specified directly as " + testACM0,
			},
		}}, configs[0].Status.Sources)
	})

	t.Run("records match failure and notifies status updater", func(t *testing.T) {
		hwConfig := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
				Profile: ptpv2alpha1.HardwareProfile{
					ClockChain: &ptpv2alpha1.ClockChain{
						Behavior: &ptpv2alpha1.Behavior{
							Sources: []ptpv2alpha1.SourceConfig{{
								Name:       testSourceGNSS,
								SourceType: ptpv2alpha1.SourceTypeGNSS,
								GNSSConfig: &ptpv2alpha1.GNSSConfig{Match: &ptpv2alpha1.GNSSMatcher{}},
							}},
						},
					},
				},
			},
		}
		hcm := makeTestHCM(hwConfig)
		statusUpdates := 0
		hcm.SetGNSSStatusChangedHandler(func() { statusUpdates++ })

		port, err := hcm.GetGNSSSerialPort(testProfile(testProfileName))
		assert.Empty(t, port)
		assert.Error(t, err)
		configs := hcm.CloneHardwareConfigs()
		assert.Equal(t, []ptpv2alpha1.SourceStatus{{
			Name: testSourceGNSS,
			Gnss: &ptpv2alpha1.GNSSStatus{
				MatchResult: "GNSS device matching failed: GNSSMatcher has neither ttyDevice, serialDevice, ethernetDevice, nor usbDevice set",
			},
		}}, configs[0].Status.Sources)
		assert.Equal(t, 1, statusUpdates)

		_, err = hcm.GetGNSSSerialPort(testProfile(testProfileName))
		assert.Error(t, err)
		assert.Equal(t, 1, statusUpdates, "unchanged match failures should not trigger duplicate status updates")
	})

	t.Run("handles GNSS source with missing GNSSConfig", func(t *testing.T) {
		hwConfig := ptpv2alpha1.HardwareConfig{
			Spec: ptpv2alpha1.HardwareConfigSpec{
				RelatedPtpProfileName: testHWConfigName,
				Profile: ptpv2alpha1.HardwareProfile{
					ClockChain: &ptpv2alpha1.ClockChain{
						Behavior: &ptpv2alpha1.Behavior{
							Sources: []ptpv2alpha1.SourceConfig{{
								Name:       testSourceGNSS,
								SourceType: ptpv2alpha1.SourceTypeGNSS,
							}},
						},
					},
				},
			},
		}
		hcm := makeTestHCM(hwConfig)
		statusUpdates := 0
		hcm.SetGNSSStatusChangedHandler(func() { statusUpdates++ })

		port, err := hcm.GetGNSSSerialPort(testProfile(testProfileName))
		assert.Empty(t, port)
		assert.EqualError(t, err, "no GNSS configuration defined")
		assert.Equal(t, []ptpv2alpha1.SourceStatus{{
			Name: testSourceGNSS,
			Gnss: &ptpv2alpha1.GNSSStatus{
				MatchResult: "GNSS device matching failed: no GNSS configuration defined",
			},
		}}, hcm.CloneHardwareConfigs()[0].Status.Sources)
		assert.Equal(t, 1, statusUpdates)
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

	t.Run("resolves EthernetDevice name via sysfs", func(t *testing.T) {
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
										Match: &ptpv2alpha1.GNSSMatcher{EthernetDevice: &ptpv2alpha1.EthernetDevice{Name: testIfaceEno8703}},
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

func TestDescribeGNSSMatch(t *testing.T) {
	assert.Equal(t, "Matched GNSS tty exposed by USB device 1546:01a9", describeGNSSMatch(&ptpv2alpha1.GNSSMatcher{
		USBDevice: &ptpv2alpha1.USBDevice{Vendor: "1546", Product: "01a9"},
	}))
	assert.Equal(t, "Matched GNSS tty exposed by USB device 1546:01a9 at path 2-1.4", describeGNSSMatch(&ptpv2alpha1.GNSSMatcher{
		USBDevice: &ptpv2alpha1.USBDevice{Vendor: "1546", Product: "01a9", Path: "2-1.4"},
	}))
}

func TestGetGNSSInitConfigMapsConfiguredSettings(t *testing.T) {
	gnssConfig := &ptpv2alpha1.GNSSConfig{
		Init: ptpv2alpha1.GNSSInit{
			AntennaVoltage: true,
			Constellations: []ptpv2alpha1.ConstellationID{
				ptpv2alpha1.ConstellationGPS,
				ptpv2alpha1.ConstellationGalileo,
				ptpv2alpha1.ConstellationGLONASS,
				ptpv2alpha1.ConstellationBeiDou,
				ptpv2alpha1.ConstellationSBAS,
				ptpv2alpha1.ConstellationID("unknown"),
			},
			SurveyIn: ptpv2alpha1.GNSSSurveyParameters{ObservationTime: 600, Accuracy: 5},
			ExtraCommands: []ptpv2alpha1.UBLXCommand{
				{Args: []string{"-p", testMonHW}, Record: true},
				{Args: []string{"-p", testCfgMsg}},
			},
		},
	}
	hwConfig := ptpv2alpha1.HardwareConfig{
		Spec: ptpv2alpha1.HardwareConfigSpec{
			RelatedPtpProfileName: testHWConfigName,
			Profile: ptpv2alpha1.HardwareProfile{
				ClockChain: &ptpv2alpha1.ClockChain{
					Behavior: &ptpv2alpha1.Behavior{
						Sources: []ptpv2alpha1.SourceConfig{{
							Name: testSourceGNSS, SourceType: ptpv2alpha1.SourceTypeGNSS, GNSSConfig: gnssConfig,
						}},
					},
				},
			},
		},
	}
	hcm := makeTestHCM(hwConfig)

	config := hcm.GetGNSSInitConfig(testProfile(testProfileName))
	if assert.NotNil(t, config) {
		assert.True(t, config.AntennaVoltage)
		assert.Equal(t, []ublox.Constellation{
			ublox.ConstellationGPS,
			ublox.ConstellationQZSS,
			ublox.ConstellationGalileo,
			ublox.ConstellationGLONASS,
			ublox.ConstellationBeiDou,
			ublox.ConstellationSBAS,
		}, config.Constellations)
		assert.Equal(t, &ublox.SurveyInConfig{ObservationTime: 600, AccuracyMeters: 5}, config.SurveyIn)
		assert.Equal(t, ublox.CommandList{
			{Args: []string{"-p", testMonHW}, ReportOutput: true},
			{Args: []string{"-p", testCfgMsg}},
		}, config.ExtraCommands)
	}

	t.Run("omits non-positive survey-in", func(t *testing.T) {
		gnssConfig.Init.SurveyIn.ObservationTime = 0
		surveyConfig := hcm.GetGNSSInitConfig(testProfile(testProfileName))
		if assert.NotNil(t, surveyConfig) {
			assert.Nil(t, surveyConfig.SurveyIn)
		}
	})

	t.Run("returns nil when no GNSS source is configured", func(t *testing.T) {
		assert.Nil(t, makeTestHCM().GetGNSSInitConfig(testProfile(testProfileName)))
	})
}
