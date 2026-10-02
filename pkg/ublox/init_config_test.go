package ublox

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	GPSEnabled    = "CFG-SIGNAL-GPS_ENA,1"
	GPSDisabled   = "CFG-SIGNAL-GPS_ENA,0"
	QZSSEnabled   = "CFG-SIGNAL-QZSS_ENA,1"
	QZSSDisabled  = "CFG-SIGNAL-QZSS_ENA,0"
	GALEnabled    = "CFG-SIGNAL-GAL_ENA,1"
	GALDisabled   = "CFG-SIGNAL-GAL_ENA,0"
	GLOEnabled    = "CFG-SIGNAL-GLO_ENA,1"
	GLODisabled   = "CFG-SIGNAL-GLO_ENA,0"
	BDSEnabled    = "CFG-SIGNAL-BDS_ENA,1"
	BDSDisabled   = "CFG-SIGNAL-BDS_ENA,0"
	SBASEnabled   = "CFG-SIGNAL-SBAS_ENA,1"
	SBASDisabled  = "CFG-SIGNAL-SBAS_ENA,0"
	NAVICEnabled  = "CFG-SIGNAL-NAVIC_ENA,1"
	NAVICDisabled = "CFG-SIGNAL-NAVIC_ENA,0"
)

func TestEnableConstellation(t *testing.T) {
	tests := []struct {
		name          string
		constellation Constellation
		enable        bool
		want          string
	}{
		{
			name:          "enable GPS",
			constellation: ConstellationGPS,
			enable:        true,
			want:          GPSEnabled,
		},
		{
			name:          "disable Galileo",
			constellation: ConstellationGalileo,
			want:          GALDisabled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, enableConstellation(tt.constellation, tt.enable))
		})
	}
}

func TestBuildConstellationCommand(t *testing.T) {
	tests := []struct {
		name           string
		protoVersion   string
		constellations []Constellation
		wantArgs       []string
	}{
		{
			name:           "29.20 enables GPS and disables other constellations",
			protoVersion:   ProtoVersion29dot20,
			constellations: []Constellation{ConstellationGPS},
			wantArgs: []string{
				"-z", GPSEnabled,
				"-z", QZSSDisabled,
				"-z", GALDisabled,
				"-z", GLODisabled,
				"-z", BDSDisabled,
				"-z", SBASDisabled,
				"-z", NAVICDisabled,
			},
		},
		{
			name:           "29.20 preserves the supported constellation order",
			protoVersion:   ProtoVersion29dot20,
			constellations: []Constellation{ConstellationNAVIC, ConstellationGalileo, ConstellationBeiDou},
			wantArgs: []string{
				"-z", GALEnabled,
				"-z", BDSEnabled,
				"-z", NAVICEnabled,
				"-z", GPSDisabled,
				"-z", QZSSDisabled,
				"-z", GLODisabled,
				"-z", SBASDisabled,
			},
		},
		{
			name:           "29.25 omits GLONASS",
			protoVersion:   ProtoVersion29dot25,
			constellations: []Constellation{ConstellationGPS, ConstellationGLONASS},
			wantArgs: []string{
				"-z", GPSEnabled,
				"-z", QZSSDisabled,
				"-z", GALDisabled,
				"-z", BDSDisabled,
				"-z", SBASDisabled,
				"-z", NAVICDisabled,
			},
		},
		{
			name:           "29.25 omits command when only unsupported constellations are requested",
			protoVersion:   ProtoVersion29dot25,
			constellations: []Constellation{ConstellationGLONASS},
			wantArgs:       nil,
		},
		{
			name:         "empty configuration uses defaults",
			protoVersion: ProtoVersion29dot20,
			wantArgs: []string{
				"-z", GPSEnabled,
				"-z", QZSSEnabled,
				"-z", GALEnabled,
				"-z", GLODisabled,
				"-z", BDSDisabled,
				"-z", SBASDisabled,
				"-z", NAVICDisabled,
			},
		},
		{
			name:           "unknown protocol has no version specific disables",
			protoVersion:   "unknown",
			constellations: []Constellation{ConstellationGPS},
			wantArgs:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := &InitConfig{Constellations: tt.constellations}
			got := config.buildConstellationCommand(tt.protoVersion)
			assert.Equal(t, tt.wantArgs, got.Args)
			assert.False(t, got.ReportOutput)
		})
	}
}

func TestBuildInitCommands(t *testing.T) {
	tests := []struct {
		name         string
		protoVersion string
		config       *InitConfig
		want         CommandList
	}{
		{
			name:   "nil config",
			config: nil,
		},
		{
			name:         "antenna voltage disabled",
			protoVersion: ProtoVersion29dot25,
			config:       &InitConfig{},
			want: CommandList{
				{Args: []string{"-z", "CFG-HW-ANT_CFG_VOLTCTRL,0"}},
				{Args: []string{
					"-z", GPSEnabled,
					"-z", QZSSEnabled,
					"-z", GALEnabled,
					"-z", BDSDisabled,
					"-z", SBASDisabled,
					"-z", NAVICDisabled,
				}},
			},
		},
		{
			name:         "survey in and extra commands are appended",
			protoVersion: ProtoVersion29dot25,
			config: &InitConfig{
				AntennaVoltage: true,
				Constellations: []Constellation{ConstellationGPS, ConstellationQZSS},
				SurveyIn: &SurveyInConfig{
					ObservationTime: 600,
					AccuracyMeters:  5,
				},
				ExtraCommands: CommandList{
					{Args: []string{"-p", "MON-RF"}, ReportOutput: true},
				},
			},
			want: CommandList{
				{Args: []string{"-z", "CFG-HW-ANT_CFG_VOLTCTRL,1"}},
				{Args: []string{
					"-z", GPSEnabled,
					"-z", QZSSEnabled,
					"-z", GALDisabled,
					"-z", BDSDisabled,
					"-z", SBASDisabled,
					"-z", NAVICDisabled,
				}},
				{Args: []string{
					"-t", "-w", "5", "-v", "1", "-e", "SURVEYIN,600,50000",
				}, ReportOutput: true},
				{Args: []string{"-p", "MON-RF"}, ReportOutput: true},
			},
		},
		{
			name:         "non-positive survey duration is omitted",
			protoVersion: ProtoVersion29dot20,
			config: &InitConfig{
				SurveyIn: &SurveyInConfig{
					ObservationTime: 0,
					AccuracyMeters:  5,
				},
			},
			want: CommandList{
				{Args: []string{"-z", "CFG-HW-ANT_CFG_VOLTCTRL,0"}},
				{Args: []string{
					"-z", GPSEnabled,
					"-z", QZSSEnabled,
					"-z", GALEnabled,
					"-z", GLODisabled,
					"-z", BDSDisabled,
					"-z", SBASDisabled,
					"-z", NAVICDisabled,
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, BuildInitCommands(tt.protoVersion, tt.config))
		})
	}
}
