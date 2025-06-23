package parser

import (
	"regexp"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
)

func TestPHC2SYSParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		regex          *regexp.Regexp
		expectedSkip   bool
		expectedResult *phc2sysParsed
	}{
		// Summary metrics
		{
			name:    "Valid summary metrics for CLOCK_REALTIME",
			regex:   summaryPhc2SysRegex,
			logLine: "phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms 4 max 4 freq -76829 +/- 0 delay 1085 +/- 0",
			expectedResult: &phc2sysParsed{
				Timestamp:  "3560354.300",
				ConfigName: "ptp4l.0.config",
				ClockName:  constants.ClockRealTime,
				Offset:     _ptr(4.0),
				MaxOffset:  _ptr(4.0),
				FreqAdj:    _ptr(-76829.0),
				Delay:      _ptr(1085.0),
				Source:     "",
			},
		},
		{
			name:           "Valid summary metrics for interface but we ignore anything besides CLOCK_REALTIME",
			regex:          summaryPhc2SysRegex,
			logLine:        "phc2sys[5196755.139]: [ptp4l.0.config] ens5f0 rms 3152778 max 3152778 freq -6083928 +/- 0 delay 2791 +/- 0",
			expectedResult: nil,
		},
		{
			name:         "Invalid log line summary",
			regex:        summaryPhc2SysRegex,
			logLine:      "invalid log line",
			expectedSkip: true,
		},
		{
			name:         "Empty log line summary",
			regex:        summaryPhc2SysRegex,
			logLine:      "",
			expectedSkip: true,
		},

		// Regular metrics
		{
			name:    "Valid regular metrics with phc offset",
			regex:   regularPhc2SysRegex,
			logLine: "phc2sys[10522413.392]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset 8 s2 freq -6990 delay 502",
			expectedResult: &phc2sysParsed{
				Timestamp:      "10522413.392",
				ConfigName:     "ptp4l.0.config",
				ServerityLevel: _ptr(6),
				ClockName:      constants.ClockRealTime,
				Offset:         _ptr(8.0),
				FreqAdj:        _ptr(-6990.0),
				Delay:          _ptr(502.0),
				ServoState:     "s2",
				Source:         constants.Phc,
			},
		},
		{
			name:    "Valid regular metrics with sys offset",
			regex:   regularPhc2SysRegex,
			logLine: "phc2sys[10522413.392]: [ptp4l.0.config:6] CLOCK_REALTIME sys offset 8 s2 freq -6990 delay 502",
			expectedResult: &phc2sysParsed{
				Timestamp:      "10522413.392",
				ConfigName:     "ptp4l.0.config",
				ServerityLevel: _ptr(6),
				ClockName:      constants.ClockRealTime,
				Offset:         _ptr(8.0),
				FreqAdj:        _ptr(-6990.0),
				Delay:          _ptr(502.0),
				ServoState:     "s2",
				Source:         constants.Sys,
			},
		},
		{
			name:         "Invalid log line",
			regex:        regularPhc2SysRegex,
			logLine:      "invalid log line",
			expectedSkip: true,
		},
		{
			name:         "Empty log line",
			regex:        regularPhc2SysRegex,
			logLine:      "",
			expectedSkip: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewPhc2SysExtractor()

			res, parsed, err := parseLine(tt.logLine, tt.regex, extractor.NewParsed)
			if err != nil {
				t.Errorf("unexpected error in  extraction: %v", err)
			}

			if tt.expectedSkip {
				assert.False(t, parsed)
			} else if tt.expectedResult != nil {
				assert.NotNil(t, res)

				assert.Equal(t, tt.expectedResult.Offset, res.Offset, "incorrect Offset")
				assert.Equal(t, tt.expectedResult.MaxOffset, res.MaxOffset, "incorrect MaxOffset")
				assert.Equal(t, tt.expectedResult.FreqAdj, res.FreqAdj, "incorrect FreqAdj")
				assert.Equal(t, tt.expectedResult.Delay, res.Delay, "incorrect Delay")
				assert.Equal(t, tt.expectedResult.ServoState, res.ServoState, "incorrect ServoState")
				assert.Equal(t, tt.logLine, res.Raw, "incorrect Raw")
			}
		})
	}
}
