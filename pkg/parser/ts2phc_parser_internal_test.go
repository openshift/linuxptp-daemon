package parser

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTS2PHCParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		regex          *regexp.Regexp
		expectedSkip   bool
		expectedResult *ts2phcParsed
	}{
		// Summary metrics
		{
			name:    "Valid metrics for master",
			regex:   regularTS2PhcRegex,
			logLine: "ts2phc[82674.465]: [ts2phc.0.cfg] ens2f1 master offset        2345 s2 freq      -16642",
			expectedResult: &ts2phcParsed{
				Timestamp:  "82674.465",
				ConfigName: "ts2phc.0.cfg",
				Interface:  "ens2f1",
				ClockName:  "master",
				Offset:     _ptr(2345.0),
				ServoState: "s2",
				FreqAdj:    _ptr(-16642.0),
			},
		},
		{
			name:    "Valid metrics for clock path",
			regex:   regularTS2PhcRegex,
			logLine: "ts2phc[82674.4656]: [ts2phc.0.cfg] /dev/ptp6 offset        3152778 s2 freq      -3152778",
			expectedResult: &ts2phcParsed{
				Timestamp:  "82674.4656",
				ConfigName: "ts2phc.0.cfg",
				Interface:  "/dev/ptp6",
				Offset:     _ptr(3152778.0),
				ServoState: "s2",
				FreqAdj:    _ptr(-3152778.0),
			},
		},
		{
			name:    "Valid metrics with master offset with holdover",
			regex:   regularTS2PhcRegex,
			logLine: "ts2phc[521734.693]: [ts2phc.0.config:6] ens2f2 offset      -1 s2 freq      -3972 holdover",
			expectedResult: &ts2phcParsed{
				Timestamp:     "521734.693",
				ConfigName:    "ts2phc.0.config",
				SeverityLevel: _ptr(6),
				Interface:     "ens2f2",
				Offset:        _ptr(-1.0),
				ServoState:    "s2",
				FreqAdj:       _ptr(-3972.0),
				Holdover:      "holdover",
			},
		},
		{
			name:         "Invalid log line",
			regex:        regularTS2PhcRegex,
			logLine:      "invalid log line",
			expectedSkip: true,
		},
		{
			name:         "Empty log line",
			regex:        regularTS2PhcRegex,
			logLine:      "",
			expectedSkip: true,
		},

		// NMEA status metrics
		{
			name:    "Valid NMEA status with value 1",
			regex:   nmeaStatusTS2PhcRegex,
			logLine: "ts2phc[1726600506]:[ts2phc.0.config] ens7f0 nmea_status 1 offset 0 s2",
			expectedResult: &ts2phcParsed{
				Timestamp:  "1726600506",
				ConfigName: "ts2phc.0.config",
				Interface:  "ens7f0",
				Status:     _ptr(1.0),
				Offset:     _ptr(0.0),
				ServoState: "s2",
			},
		},
		{
			name:    "Valid NMEA status with value 0",
			regex:   nmeaStatusTS2PhcRegex,
			logLine: "ts2phc[1699929121]:[ts2phc.0.config] ens2f0 nmea_status 0 offset 999999 s0",
			expectedResult: &ts2phcParsed{
				Timestamp:  "1699929121",
				ConfigName: "ts2phc.0.config",
				Interface:  "ens2f0",
				Status:     _ptr(0.0),
				Offset:     _ptr(999999.0),
				ServoState: "s0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewTS2PHCExtractor()

			res, parsed, err := parseLine(tt.logLine, tt.regex, extractor.NewParsed)
			if err != nil {
				t.Errorf("unexpected error in  extraction: %v", err)
			}

			if tt.expectedSkip {
				assert.False(t, parsed)
			} else if tt.expectedResult != nil {
				assert.NotNil(t, res)
				assert.Equal(t, tt.expectedResult.Timestamp, res.Timestamp, "incorrect Timestamp")
				assert.Equal(t, tt.expectedResult.ConfigName, res.ConfigName, "incorrect ConfigName")
				assert.Equal(t, tt.expectedResult.SeverityLevel, res.SeverityLevel, "incorrect ServerityLevel")
				assert.Equal(t, tt.expectedResult.Interface, res.Interface, "incorrect Interface")
				assert.Equal(t, tt.expectedResult.ClockName, res.ClockName, "incorrect ClockName")
				assert.Equal(t, tt.expectedResult.Status, res.Status, "incorrect Status")
				assert.Equal(t, tt.expectedResult.Offset, res.Offset, "incorrect Offset")
				assert.Equal(t, tt.expectedResult.ServoState, res.ServoState, "incorrect ServoState")
				assert.Equal(t, tt.expectedResult.FreqAdj, res.FreqAdj, "incorrect FreqAdj")
				assert.Equal(t, tt.expectedResult.Holdover, res.Holdover, "incorrect Holdover")
				assert.Equal(t, tt.logLine, res.Raw, "incorrect Raw")
			}
		})
	}
}
