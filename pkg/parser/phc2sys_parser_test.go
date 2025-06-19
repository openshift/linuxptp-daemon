package parser_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
)

func TestPhc2SysParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		configName     string
		expectedError  bool
		expectedMetric *parser.Metrics
	}{
		{
			name:       "Valid summary metrics for CLOCK_REALTIME",
			configName: "ptp4l.0.config",
			logLine:    "phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms 4 max 4 freq -76829 +/- 0 delay 1085 +/- 0",
			expectedMetric: &parser.Metrics{
				Iface:      "CLOCK_REALTIME",
				Offset:     4,
				MaxOffset:  4,
				FreqAdj:    -76829,
				Delay:      1085,
				ClockState: "",
				Source:     constants.Master,
			},
		},
		{
			name:           "Valid summary metrics for interface but we ignore anything besides CLOCK_REALTIME",
			configName:     "ptp4l.0.config",
			logLine:        "phc2sys[5196755.139]: [ptp4l.0.config] ens5f0 rms 3152778 max 3152778 freq -6083928 +/- 0 delay 2791 +/- 0",
			expectedMetric: nil,
			expectedError:  false, // just skip over it
		},
		{
			name:       "Valid regular metrics with phc offset",
			configName: "ptp4l.0.config",
			logLine:    "phc2sys[10522413.392]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset 8 s2 freq -6990 delay 502",
			expectedMetric: &parser.Metrics{
				Iface:      "CLOCK_REALTIME",
				Offset:     8,
				MaxOffset:  8,
				FreqAdj:    -6990,
				Delay:      502,
				ClockState: constants.ClockStateLocked,
				Source:     constants.Phc,
			},
		},
		{
			name:       "Valid regular metrics with sys offset",
			configName: "ptp4l.0.config",
			logLine:    "phc2sys[10522413.392]: [ptp4l.0.config:6] CLOCK_REALTIME sys offset 8 s2 freq -6990 delay 502",
			expectedMetric: &parser.Metrics{
				Iface:      "CLOCK_REALTIME",
				Offset:     8,
				MaxOffset:  8,
				FreqAdj:    -6990,
				Delay:      502,
				ClockState: constants.ClockStateLocked,
				Source:     constants.Sys,
			},
		},
		// With current approch these are just skipped
		// {
		// 	name:          "Invalid log line",
		// 	configName:    "ptp4l.0.config",
		// 	logLine:       "invalid log line",
		// 	expectedError: true,
		// },
		// {
		// 	name:          "Empty log line",
		// 	configName:    "ptp4l.0.config",
		// 	logLine:       "",
		// 	expectedError: true,
		// },
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := parser.NewPhc2SysExtractor()

			// Try both summary and regular extraction
			metric, _, err := extractor.Extract(tt.logLine)
			if err != nil && !tt.expectedError {
				t.Errorf("unexpected error in  extraction: %v", err)
			}
			if tt.expectedError {
				assert.Error(t, err)
				return
			}

			if tt.expectedMetric != nil {
				assert.NotNil(t, metric)
				assert.Equal(t, tt.expectedMetric.Iface, metric.Iface)
				assert.Equal(t, tt.expectedMetric.Offset, metric.Offset)
				assert.Equal(t, tt.expectedMetric.MaxOffset, metric.MaxOffset)
				assert.Equal(t, tt.expectedMetric.FreqAdj, metric.FreqAdj)
				assert.Equal(t, tt.expectedMetric.Delay, metric.Delay)
				assert.Equal(t, tt.expectedMetric.ClockState, metric.ClockState)
				assert.Equal(t, tt.expectedMetric.Source, metric.Source)
			}
		})
	}
}

// These are just skipped
// func TestPhc2SysParserErrorCases(t *testing.T) {
// 	extractor := NewPhc2SysExtractor()

// 	tests := []struct {
// 		name        string
// 		logLine     string
// 		configName  string
// 		expectError bool
// 	}{
// 		{
// 			name:        "Malformed summary metrics",
// 			logLine:     "phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms invalid max 4 freq -76829",
// 			configName:  "ptp4l.0.config",
// 			expectError: true,
// 		},
// 		{
// 			name:        "Malformed regular metrics",
// 			logLine:     "phc2sys[10522413.392]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset invalid s2 freq -6990",
// 			configName:  "ptp4l.0.config",
// 			expectError: true,
// 		},
// 		{
// 			name:        "Missing required fields",
// 			logLine:     "phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME",
// 			configName:  "ptp4l.0.config",
// 			expectError: true,
// 		},
// 	}

// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			_, _, err := extractor.Extract(tt.logLine)
// 			if tt.expectError {
// 				assert.Error(t, err)
// 			} else {
// 				assert.NoError(t, err)
// 			}

// 			_, _, err = extractor.Extract(tt.logLine)
// 			if tt.expectError {
// 				assert.Error(t, err)
// 			} else {
// 				assert.NoError(t, err)
// 			}
// 		})
// 	}
// }
