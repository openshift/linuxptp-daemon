package parser_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChronydParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		expectedMetric *parser.Metrics
		noMatch        bool
	}{
		{
			name:    "Selected source locks clock",
			logLine: "chronyd[12345][chronyd.0.config]: Selected source 192.168.1.1 (1.2.3.4)",
			expectedMetric: &parser.Metrics{
				Iface:      constants.ClockRealTime,
				ClockState: constants.ClockStateLocked,
				Offset:     0,
				Source:     constants.CHRONYD,
			},
		},
		{
			name:    "No selectable sources means freerun",
			logLine: "chronyd[12345][chronyd.0.config]: Can't synchronise: no selectable sources",
			expectedMetric: &parser.Metrics{
				Iface:      constants.ClockRealTime,
				ClockState: constants.ClockStateFreeRun,
				Offset:     0,
				Source:     constants.CHRONYD,
			},
		},
		{
			name:    "Selected source with different config index",
			logLine: "chronyd[99][chronyd.1.config]: Selected source 10.0.0.1",
			expectedMetric: &parser.Metrics{
				Iface:      constants.ClockRealTime,
				ClockState: constants.ClockStateLocked,
				Offset:     0,
				Source:     constants.CHRONYD,
			},
		},
		{
			name:    "Unrelated chronyd log line",
			logLine: "chronyd[12345][chronyd.0.config]: System clock wrong by 0.000001 seconds",
			noMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := parser.NewChronydExtractor()
			metric, _, err := extractor.Extract(tt.logLine)

			if tt.noMatch {
				assert.Nil(t, metric)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, metric)
			assert.Equal(t, tt.expectedMetric.Iface, metric.Iface)
			assert.Equal(t, tt.expectedMetric.ClockState, metric.ClockState)
			assert.Equal(t, tt.expectedMetric.Offset, metric.Offset)
			assert.Equal(t, tt.expectedMetric.Source, metric.Source)
		})
	}
}
