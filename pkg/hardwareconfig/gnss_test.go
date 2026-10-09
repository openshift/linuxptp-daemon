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
