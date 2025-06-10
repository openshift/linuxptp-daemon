package parser

import (
	"testing"

	sstate "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/state"
	"github.com/stretchr/testify/assert"
)

func TestPTP4LParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		configName     string
		expectedError  bool
		expectedMetric *Metrics
		state          *sstate.SharedState
	}{
		{
			name:       "Valid summary metrics for master",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[74737.942]: [ptp4l.0.config] rms 53 max 74 freq -16642 +/- 40 delay 1089 +/- 20",
			state:      nil,
			expectedMetric: &Metrics{
				Iface:      master,
				Offset:     53,
				MaxOffset:  74,
				FreqAdj:    -16642,
				Delay:      1089,
				ClockState: "",
				Source:     master,
			},
		},
		{
			name:       "Valid summary metrics for interface",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5196755.139]: [ptp4l.0.config] ens5f0 rms 3152778 max 3152778 freq -6083928 +/- 0 delay 2791 +/- 0",
			state:      nil,
			expectedMetric: &Metrics{
				Iface:      "ens5f0",
				Offset:     3152778,
				MaxOffset:  3152778,
				FreqAdj:    -6083928,
				Delay:      2791,
				ClockState: "",
				Source:     master,
			},
		},
		{
			name:       "Valid regular metrics with master offset",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[365195.391]: [ptp4l.0.config] master offset -1 s2 freq -3972 path delay 89",
			state:      sstate.NewSharedState(),
			expectedMetric: &Metrics{
				Iface:      master,
				Offset:     -1,
				MaxOffset:  -1,
				FreqAdj:    -3972,
				Delay:      89,
				ClockState: LOCKED,
				Source:     master,
			},
		},
		{
			name:           "Invalid log line",
			configName:     "ptp4l.0.config",
			logLine:        "invalid log line",
			expectedError:  true,
			expectedMetric: nil,
		},
		{
			name:           "Empty log line",
			configName:     "ptp4l.0.config",
			logLine:        "",
			expectedError:  true,
			expectedMetric: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := sstate.NewSharedState()
			if tt.state != nil {
				err := state.SetMasterOffsetIface("ptp4l.0.config", tt.expectedMetric.Iface)
				if err != nil {
					return
				}
			}
			extractor := NewPTP4LExtractor(state)

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
				assert.Equal(t, tt.expectedMetric.Delay, metric.Delay)
				assert.Equal(t, tt.expectedMetric.ClockState, metric.ClockState)
				assert.Equal(t, tt.expectedMetric.Source, metric.Source)
			}

		})
	}
}

func TestPTP4LEventParser(t *testing.T) {
	tests := []struct {
		name          string
		logLine       string
		configName    string
		expectedError bool
		expectedEvent *PTPEvent
	}{
		{
			name:       "Port state change to SLAVE",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER",
			expectedEvent: &PTPEvent{
				PortID: 1,
				Role:   SLAVE,
				Raw:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER",
			},
		},
		{
			name:       "Port state change to PASSIVE",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to PASSIVE on RS_PASSIVE",
			expectedEvent: &PTPEvent{
				PortID: 1,
				Role:   PASSIVE,
				Raw:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to PASSIVE on RS_PASSIVE",
			},
		},
		{
			name:       "Port state change to MASTER",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to MASTER on RS_MASTER",
			expectedEvent: &PTPEvent{
				PortID: 1,
				Role:   MASTER,
				Raw:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to MASTER on RS_MASTER",
			},
		},
		{
			name:       "Port state change to FAULTY",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: FAULT_DETECTED",
			expectedEvent: &PTPEvent{
				PortID: 1,
				Role:   FAULTY,
				Raw:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: FAULT_DETECTED",
			},
		},
		{
			name:       "Port state change to LISTENING",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to LISTENING on RS_LISTENING",
			expectedEvent: &PTPEvent{
				PortID: 1,
				Role:   LISTENING,
				Raw:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to LISTENING on RS_LISTENING",
			},
		},
		{
			name:          "Invalid port state change",
			configName:    "ptp4l.0.config",
			logLine:       "ptp4l[4268779.809]: [ptp4l.0.config] port 1: INVALID_STATE",
			expectedError: true,
		},
		{
			name:          "Invalid port number",
			configName:    "ptp4l.0.config",
			logLine:       "ptp4l[4268779.809]: [ptp4l.0.config] port invalid: UNCALIBRATED to SLAVE",
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := sstate.NewSharedState()
			extractor := NewPTP4LExtractor(state)

			_, event, err := extractor.Extract(tt.logLine)
			if tt.expectedError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			if tt.expectedEvent != nil {
				assert.NotNil(t, event)
				assert.Equal(t, tt.expectedEvent.PortID, event.PortID)
				assert.Equal(t, tt.expectedEvent.Role, event.Role)
				assert.Equal(t, tt.expectedEvent.Raw, event.Raw)
			}
		})
	}
}

func TestPTP4LParserErrorCases(t *testing.T) {
	state := sstate.NewSharedState()
	extractor := NewPTP4LExtractor(state)

	tests := []struct {
		name        string
		logLine     string
		configName  string
		expectError bool
	}{
		{
			name:        "Malformed summary metrics",
			logLine:     "ptp4l[74737.942]: [ptp4l.0.config] rms invalid max 74 freq -16642",
			configName:  "ptp4l.0.config",
			expectError: true,
		},
		{
			name:        "Malformed regular metrics",
			logLine:     "ptp4l[365195.391]: [ptp4l.0.config] master offset invalid s2 freq -3972",
			configName:  "ptp4l.0.config",
			expectError: true,
		},
		{
			name:        "Missing required fields",
			logLine:     "ptp4l[74737.942]: [ptp4l.0.config]",
			configName:  "ptp4l.0.config",
			expectError: true,
		},
		{
			name:        "Invalid clock state",
			logLine:     "ptp4l[365195.391]: [ptp4l.0.config] master offset -1 invalid freq -3972",
			configName:  "ptp4l.0.config",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := extractor.ExtractSummaryFn(tt.logLine)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			_, err = extractor.ExtractRegularFn(tt.logLine)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
