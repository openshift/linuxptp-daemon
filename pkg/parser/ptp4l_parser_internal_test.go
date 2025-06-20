package parser

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

func _ptr[T any](x T) *T {
	return &x
}

func TestPTP4LParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		regex          *regexp.Regexp
		expectedSkip   bool
		expectedResult *ptp4lParsed
	}{
		// Summary metrics
		{
			name:    "Valid summary metrics for master",
			logLine: "ptp4l[74737.942]: [ptp4l.0.config] rms 53 max 74 freq -16642 +/- 40 delay 1089 +/- 20",
			regex:   summaryPTP4LRegex,
			expectedResult: &ptp4lParsed{
				Timestamp:  "74737.942",
				ConfigName: "ptp4l.0.config",
				Interface:  "",
				Offset:     _ptr(53.0),
				MaxOffset:  _ptr(74.0),
				FreqAdj:    _ptr(-16642.0),
				Delay:      _ptr(1089.0),
				ServoState: "",
			},
		},
		{
			name:    "Valid summary metrics for interface",
			logLine: "ptp4l[5196755.139]: [ptp4l.0.config] ens5f0 rms 3152778 max 3152778 freq -6083928 +/- 0 delay 2791 +/- 0",
			regex:   summaryPTP4LRegex,
			expectedResult: &ptp4lParsed{
				Timestamp:  "5196755.139",
				ConfigName: "ptp4l.0.config",
				Interface:  "ens5f0",
				Offset:     _ptr(3152778.0),
				MaxOffset:  _ptr(3152778.0),
				FreqAdj:    _ptr(-6083928.0),
				Delay:      _ptr(2791.0),
				ServoState: "",
			},
		},
		{
			name:           "Invalid log line summary",
			logLine:        "invalid log line",
			regex:          summaryPTP4LRegex,
			expectedSkip:   true,
			expectedResult: nil,
		},

		// Regular metrics
		{
			name:    "Valid regular metrics with master offset",
			logLine: "ptp4l[365195.391]: [ptp4l.0.config] master offset -1 s2 freq -3972 path delay 89",
			regex:   regularPTP4LRegex,
			expectedResult: &ptp4lParsed{
				Timestamp:  "365195.391",
				ConfigName: "ptp4l.0.config",
				Interface:  "master",
				Offset:     _ptr(-1.0),
				FreqAdj:    _ptr(-3972.0),
				Delay:      _ptr(89.0),
				ServoState: "s2",
			},
		},
		{
			name:           "Empty log line summary",
			logLine:        "",
			regex:          summaryPTP4LRegex,
			expectedSkip:   false,
			expectedResult: &ptp4lParsed{},
		},
		{
			name:           "Invalid log line regular",
			logLine:        "invalid log line",
			regex:          regularPTP4LRegex,
			expectedSkip:   true,
			expectedResult: nil,
		},
		{
			name:           "Empty log line regular",
			logLine:        "",
			regex:          regularPTP4LRegex,
			expectedSkip:   false,
			expectedResult: &ptp4lParsed{},
		},

		// ptp4l event
		{
			name:    "Port state change to SLAVE",
			logLine: "ptp4l[4268779.809]: [ptp4l.1.config] port 1: UNCALIBRATED to SLAVE on MASTER",
			regex:   ptp4lEventRegex,
			expectedResult: &ptp4lParsed{
				Timestamp:  "4268779.809",
				ConfigName: "ptp4l.1.config",
				PortID:     _ptr(1),
				Event:      "UNCALIBRATED to SLAVE on MASTER",
			},
		},
		{
			name:    "Port state change to PASSIVE",
			logLine: "ptp4l[4268779.809]: [ptp4l.2.config] port 2: UNCALIBRATED to PASSIVE on RS_PASSIVE",
			regex:   ptp4lEventRegex,
			expectedResult: &ptp4lParsed{
				Timestamp:  "4268779.809",
				ConfigName: "ptp4l.2.config",
				PortID:     _ptr(2),
				Event:      "UNCALIBRATED to PASSIVE on RS_PASSIVE",
			},
		},
		{
			name:    "Port state change to MASTER",
			logLine: "ptp4l[4268779.809]: [ptp4l.3.config] port 3: UNCALIBRATED to MASTER on RS_MASTER",
			regex:   ptp4lEventRegex,
			expectedResult: &ptp4lParsed{
				Timestamp:  "4268779.809",
				ConfigName: "ptp4l.3.config",
				PortID:     _ptr(3),
				Event:      "UNCALIBRATED to MASTER on RS_MASTER",
			},
		},
		{
			name:    "Port state change to FAULTY",
			logLine: "ptp4l[4268779.809]: [ptp4l.4.config] port 4: FAULT_DETECTED",
			regex:   ptp4lEventRegex,
			expectedResult: &ptp4lParsed{
				Timestamp:  "4268779.809",
				ConfigName: "ptp4l.4.config",
				PortID:     _ptr(4),
				Event:      "FAULT_DETECTED",
			},
		},
		{
			name:    "Port state change to LISTENING",
			logLine: "ptp4l[4268779.809]: [ptp4l.5.config] port 5: UNCALIBRATED to LISTENING on RS_LISTENING",
			regex:   ptp4lEventRegex,
			expectedResult: &ptp4lParsed{
				Timestamp:  "4268779.809",
				ConfigName: "ptp4l.5.config",
				PortID:     _ptr(5),
				Event:      "UNCALIBRATED to LISTENING on RS_LISTENING",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewPTP4LExtractor()

			res, parsed, err := parseLine(tt.logLine, tt.regex, extractor.NewParsed)
			if err != nil {
				t.Errorf("unexpected error in  extraction: %v", err)
			}

			if tt.expectedSkip {
				assert.False(t, parsed)
			} else if tt.expectedResult != nil {
				assert.NotNil(t, res)

				assert.Equal(t, tt.expectedResult.Interface, res.Interface, "incorrect Interface")
				assert.Equal(t, tt.expectedResult.Offset, res.Offset, "incorrect Offset")
				assert.Equal(t, tt.expectedResult.MaxOffset, res.MaxOffset, "incorrect MaxOffset")
				assert.Equal(t, tt.expectedResult.FreqAdj, res.FreqAdj, "incorrect FreqAdj")
				assert.Equal(t, tt.expectedResult.Delay, res.Delay, "incorrect Delay")
				assert.Equal(t, tt.expectedResult.ServoState, res.ServoState, "incorrect ServoState")
				assert.Equal(t, tt.expectedResult.PortID, res.PortID, "incorrect PortID")
				assert.Equal(t, tt.expectedResult.Event, res.Event, "incorrect Event")
				assert.Equal(t, tt.logLine, res.Raw, "incorrect Raw")
			}
		})
	}
}
