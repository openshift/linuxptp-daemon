package parser_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/stretchr/testify/assert"
)

func TestPTP4LParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		configName     string
		expectedError  bool
		expectedMetric *parser.Metrics
	}{
		{
			name:       "Valid summary metrics for master",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[74737.942]: [ptp4l.0.config] rms 53 max 74 freq -16642 +/- 40 delay 1089 +/- 20",
			expectedMetric: &parser.Metrics{
				Iface:      constants.Master,
				Offset:     53,
				MaxOffset:  74,
				FreqAdj:    -16642,
				Delay:      1089,
				ClockState: "",
				Source:     constants.Master,
			},
		},
		{
			name:       "Valid summary metrics for interface",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5196755.139]: [ptp4l.0.config] ens5f0 rms 3152778 max 3152778 freq -6083928 +/- 0 delay 2791 +/- 0",
			expectedMetric: &parser.Metrics{
				Iface:      "ens5f0",
				Offset:     3152778,
				MaxOffset:  3152778,
				FreqAdj:    -6083928,
				Delay:      2791,
				ClockState: "",
				Source:     constants.Master,
			},
		},
		{
			name:       "Valid regular metrics with master offset",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[365195.391]: [ptp4l.0.config] master offset -1 s2 freq -3972 path delay 89",
			expectedMetric: &parser.Metrics{
				Iface:      constants.Master,
				Offset:     -1,
				MaxOffset:  -1,
				FreqAdj:    -3972,
				Delay:      89,
				ClockState: constants.ClockStateLocked,
				Source:     constants.Master,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := parser.NewPTP4LExtractor()

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
		expectedEvent *parser.PTPEvent
	}{
		{
			name:       "Port state change to SLAVE",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleSlave,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER",
			},
		},
		{
			name:       "Port state change with interface name",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[412707.219]: [ptp4l.0.config:5] port 11 (ens8f2): LISTENING to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES",
			expectedEvent: &parser.PTPEvent{
				PortID:     11,
				Role:       constants.PortRoleMaster,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[412707.219]: [ptp4l.0.config:5] port 11 (ens8f2): LISTENING to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES",
			},
		},
		{
			name:       "Port state change to PASSIVE",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to PASSIVE on RS_PASSIVE",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRolePassive,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to PASSIVE on RS_PASSIVE",
			},
		},
		{
			name:       "Port state change to MASTER",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to MASTER on RS_MASTER",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleMaster,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to MASTER on RS_MASTER",
			},
		},
		{
			name:       "Port state change to FAULTY",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: FAULT_DETECTED",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleFaulty,
				ClockState: constants.ClockStateHoldover,
				Raw:        "ptp4l[4268779.809]: [ptp4l.0.config] port 1: FAULT_DETECTED",
			},
		},
		{
			name:       "Port state change to LISTENING",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to LISTENING on RS_LISTENING",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleListening,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to LISTENING on RS_LISTENING",
			},
		},
		{
			name:       "Slave to Passive",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5000.000]: [ptp4l.0.config] port 2: SLAVE to PASSIVE on RS_PASSIVE",
			expectedEvent: &parser.PTPEvent{
				PortID:     2,
				Role:       constants.PortRolePassive,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[5000.000]: [ptp4l.0.config] port 2: SLAVE to PASSIVE on RS_PASSIVE",
			},
		},
		{
			name:       "Master to Passive",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5001.000]: [ptp4l.0.config] port 1: MASTER to PASSIVE on RS_PASSIVE",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRolePassive,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[5001.000]: [ptp4l.0.config] port 1: MASTER to PASSIVE on RS_PASSIVE",
			},
		},
		{
			name:       "Listening to Passive",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5002.000]: [ptp4l.0.config] port 1: LISTENING to PASSIVE on RS_PASSIVE",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRolePassive,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[5002.000]: [ptp4l.0.config] port 1: LISTENING to PASSIVE on RS_PASSIVE",
			},
		},
		{
			name:       "Synchronization fault",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5003.000]: [ptp4l.0.config] port 1: SYNCHRONIZATION_FAULT",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleFaulty,
				ClockState: constants.ClockStateHoldover,
				Raw:        "ptp4l[5003.000]: [ptp4l.0.config] port 1: SYNCHRONIZATION_FAULT",
			},
		},
		{
			name:       "Slave to Uncalibrated",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5004.000]: [ptp4l.0.config] port 1: SLAVE to UNCALIBRATED",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleFaulty,
				ClockState: constants.ClockStateHoldover,
				Raw:        "ptp4l[5004.000]: [ptp4l.0.config] port 1: SLAVE to UNCALIBRATED",
			},
		},
		{
			name:       "Master to Uncalibrated RS_SLAVE",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5005.000]: [ptp4l.0.config] port 1: MASTER to UNCALIBRATED on RS_SLAVE",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleFaulty,
				ClockState: constants.ClockStateHoldover,
				Raw:        "ptp4l[5005.000]: [ptp4l.0.config] port 1: MASTER to UNCALIBRATED on RS_SLAVE",
			},
		},
		{
			name:       "Listening to Uncalibrated RS_SLAVE",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5006.000]: [ptp4l.0.config] port 1: LISTENING to UNCALIBRATED on RS_SLAVE",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleFaulty,
				ClockState: constants.ClockStateHoldover,
				Raw:        "ptp4l[5006.000]: [ptp4l.0.config] port 1: LISTENING to UNCALIBRATED on RS_SLAVE",
			},
		},
		{
			name:       "Slave to Master",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5007.000]: [ptp4l.0.config] port 1: SLAVE to MASTER",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleMaster,
				ClockState: constants.ClockStateHoldover,
				Raw:        "ptp4l[5007.000]: [ptp4l.0.config] port 1: SLAVE to MASTER",
			},
		},
		{
			name:       "Slave to Grand Master",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5008.000]: [ptp4l.0.config] port 1: SLAVE to GRAND_MASTER",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleMaster,
				ClockState: constants.ClockStateHoldover,
				Raw:        "ptp4l[5008.000]: [ptp4l.0.config] port 1: SLAVE to GRAND_MASTER",
			},
		},
		{
			name:       "Slave to Listening",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5009.000]: [ptp4l.0.config] port 1: SLAVE to LISTENING",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleListening,
				ClockState: constants.ClockStateHoldover,
				Raw:        "ptp4l[5009.000]: [ptp4l.0.config] port 1: SLAVE to LISTENING",
			},
		},
		{
			name:       "Faulty to Listening",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5010.000]: [ptp4l.0.config] port 1: FAULTY to LISTENING",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleListening,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[5010.000]: [ptp4l.0.config] port 1: FAULTY to LISTENING",
			},
		},
		{
			name:       "Initializing to Listening",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[5011.000]: [ptp4l.0.config] port 1: INITIALIZING to LISTENING",
			expectedEvent: &parser.PTPEvent{
				PortID:     1,
				Role:       constants.PortRoleListening,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[5011.000]: [ptp4l.0.config] port 1: INITIALIZING to LISTENING",
			},
		},
		{
			name:       "Invalid port state change",
			configName: "ptp4l.0.config",
			logLine:    "ptp4l[4268779.809]: [ptp4l.0.config] port 1: INVALID_STATE",
			expectedEvent: &parser.PTPEvent{
				PortID:     0,
				Role:       constants.PortRoleUnknown,
				ClockState: constants.ClockStateFreeRun,
				Raw:        "ptp4l[4268779.809]: [ptp4l.0.config] port 1: INVALID_STATE",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := parser.NewPTP4LExtractor()

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
