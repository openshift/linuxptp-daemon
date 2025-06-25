package parser_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
)

func TestTS2PHCParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		expectedError  bool
		expectedMetric *parser.Metrics
	}{
		{
			name:    "Valid metrics for master",
			logLine: "ts2phc[82674.465]: [ts2phc.0.cfg] ens2f1 master offset        2345 s2 freq      -16642",
			expectedMetric: &parser.Metrics{
				Iface:      "ens2f1",
				Offset:     2345,
				MaxOffset:  2345,
				FreqAdj:    -16642,
				ClockState: constants.ClockStateLocked,
				Source:     constants.Master,
			},
		},
		{
			name:    "Valid metrics for clock path",
			logLine: "ts2phc[82674.465]: [ts2phc.0.cfg] ens2f1 master offset        3152778 s2 freq      -6083928",
			expectedMetric: &parser.Metrics{
				Iface:      "ens2f1",
				Offset:     3152778,
				MaxOffset:  3152778,
				FreqAdj:    -6083928,
				ClockState: constants.ClockStateLocked,
				Source:     constants.Master,
			},
		},
		{
			name:    "Valid metrics with master offset with holdover",
			logLine: "ts2phc[521734.693]: [ts2phc.0.config:6] /dev/ptp6 offset      -1 s2 freq      -3972 holdover",
			expectedMetric: &parser.Metrics{
				Iface:      "/dev/ptp6",
				Offset:     -1,
				MaxOffset:  -1,
				FreqAdj:    -3972,
				ClockState: constants.ClockStateHoldover,
				Source:     constants.Master,
			},
		},
		{
			name:    "Valid NMEA status with value 1",
			logLine: "ts2phc[1726600506]:[ts2phc.0.config] ens7f0 nmea_status 1 offset 0 s2",
			expectedMetric: &parser.Metrics{
				Iface:      "ens7f0",
				Offset:     0,
				MaxOffset:  0,
				ClockState: constants.ClockStateLocked,
				Source:     constants.NmeaStatus,
				Status: []parser.StatusMetric{
					{Subtype: "nmea_status", Status: 1.0},
				},
			},
		},
		{
			name:    "Valid NMEA status with value 0",
			logLine: "ts2phc[1699929121]:[ts2phc.0.config] ens2f0 nmea_status 0 offset 999999 s0",
			expectedMetric: &parser.Metrics{
				Iface:      "ens2f0",
				Offset:     999999,
				MaxOffset:  999999,
				ClockState: constants.ClockStateFreeRun,
				Source:     constants.NmeaStatus,
				Status: []parser.StatusMetric{
					{Subtype: "nmea_status", Status: 0.0},
				},
			},
		},
		{
			name:    "Valid metrics with master offset and s0 state",
			logLine: "ts2phc[1896327.319]: [ts2phc.0.config] ens2f0 master offset         3 s0 freq      4",
			expectedMetric: &parser.Metrics{
				Iface:      "ens2f0",
				Offset:     3,
				MaxOffset:  3,
				FreqAdj:    4,
				ClockState: constants.ClockStateFreeRun,
				Source:     constants.Master,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := parser.NewTS2PHCExtractor()

			// Try both summary and regular extraction
			metric, _, err := extractor.Extract(tt.logLine)
			if err != nil && !tt.expectedError {
				t.Errorf("unexpected error in  extraction: %v", err)
			}

			if tt.expectedError {
				assert.NotNil(t, err)
			} else if tt.expectedMetric != nil {
				assert.NotNil(t, metric)
				assert.Equal(t, tt.expectedMetric.Iface, metric.Iface)
				assert.Equal(t, tt.expectedMetric.Offset, metric.Offset)
				assert.Equal(t, tt.expectedMetric.MaxOffset, metric.MaxOffset)
				assert.Equal(t, tt.expectedMetric.FreqAdj, metric.FreqAdj)
				assert.Equal(t, tt.expectedMetric.ClockState, metric.ClockState)
				assert.Equal(t, tt.expectedMetric.Source, metric.Source)
				for _, expectedStatus := range tt.expectedMetric.Status {
					found := false
					for _, status := range metric.Status {
						if status.Subtype == expectedStatus.Subtype {
							found = true
							assert.Equal(t, expectedStatus.Status, status.Status, "incorrect Status")
						}
					}
					assert.True(t, found, "Status not found")
				}
			}
		})
	}
}
