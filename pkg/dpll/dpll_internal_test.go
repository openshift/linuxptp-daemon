package dpll

import (
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	nl "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
)

func TestDpllFlags(t *testing.T) {
	tests := []struct {
		name           string
		flags          Flag
		hasPhase       bool
		hasFreq        bool
		hasOffset      bool
		expectedStrs   []string
		expectedOffStr string
	}{
		{"NoFlags", 0, true, true, true, []string{}, "100"},
		{"NoPhaseStatus", FlagNoPhaseStatus, false, true, true, []string{"NoPhaseStatus"}, "100"},
		{"NoFrequencyStatus", FlagNoFreqencyStatus, true, false, true, []string{"NoFrequencyStatus"}, "100"},
		{"NoPhaseOffset", FlagNoPhaseOffset, true, true, false, []string{"NoPhaseOffset"}, "UNKNOWN"},
		{"OnlyPhaseStatus", FlagOnlyPhaseStatus, true, false, false, []string{"NoFrequencyStatus", "NoPhaseOffset"}, "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &DpllConfig{flags: tt.flags, phaseOffset: 100}
			assert.Equal(t, tt.hasFreq, !d.hasFlag(FlagNoFreqencyStatus), "has frequency status")
			assert.Equal(t, tt.hasPhase, !d.hasFlag(FlagNoPhaseStatus), "has phase status")
			assert.Equal(t, tt.hasOffset, !d.hasFlag(FlagNoPhaseOffset), "has phase offset")
			assert.ElementsMatch(t, tt.expectedStrs, d.flagsToStrings())
			assert.Equal(t, tt.expectedOffStr, d.phaseOffsetStr())
		})
	}
}

func TestDpllStateDecisionWithFlags(t *testing.T) {
	tests := []struct {
		name          string
		flags         Flag
		phaseStatus   int64
		freqStatus    int64
		expectedState int64
	}{
		{"NoFlags_WorstIsFreq", 0, DPLL_LOCKED, DPLL_FREERUN, DPLL_FREERUN},
		{"NoFlags_WorstIsPhase", 0, DPLL_HOLDOVER, DPLL_LOCKED, DPLL_HOLDOVER},
		{"NoPhaseStatus", FlagNoPhaseStatus, DPLL_LOCKED, DPLL_FREERUN, DPLL_FREERUN},
		{"NoFrequencyStatus", FlagNoFreqencyStatus, DPLL_LOCKED, DPLL_FREERUN, DPLL_LOCKED},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &DpllConfig{
				flags:           tt.flags,
				phaseStatus:     tt.phaseStatus,
				frequencyStatus: tt.freqStatus,
				dependsOn:       []event.EventSource{event.GNSS},
			}
			assert.Equal(t, tt.expectedState, d.getDpllState())
		})
	}
}

func TestDpllOffsetChecksWithFlags(t *testing.T) {
	d := &DpllConfig{
		flags:                  FlagNoPhaseOffset,
		phaseOffset:            10000,
		LocalMaxHoldoverOffSet: 100,
		MaxInSpecOffset:        100,
		processConfig: config.ProcessConfig{
			GMThreshold: config.Threshold{Min: 0, Max: 100},
		},
	}
	assert.True(t, d.isMaxHoldoverOffsetInRange())
	assert.True(t, d.isInSpecOffsetInRange())
	assert.True(t, d.isOffsetInRange())

	d.flags = 0
	assert.False(t, d.isMaxHoldoverOffsetInRange())
	assert.False(t, d.isInSpecOffsetInRange())
	assert.False(t, d.isOffsetInRange())
}

func TestDpllSendEventWithFlags(t *testing.T) {
	eventChannel := make(chan event.EventChannel, 10)
	d := &DpllConfig{
		iface:           "test-iface",
		flags:           FlagOnlyPhaseStatus, // No freq, no offset
		phaseStatus:     DPLL_LOCKED,
		frequencyStatus: DPLL_FREERUN,
		phaseOffset:     1000,
		processConfig: config.ProcessConfig{
			EventChannel: eventChannel,
			ConfigName:   "test-config",
		},
		dependsOn: []event.EventSource{event.GNSS},
	}

	d.sendDpllEvent()

	select {
	case e := <-eventChannel:
		assert.Equal(t, event.DPLL, e.ProcessName)
		assert.Equal(t, "test-iface", e.IFace)

		_, hasFreq := e.Values[event.FREQUENCY_STATUS]
		assert.False(t, hasFreq, "should not have frequency status")

		_, hasOffset := e.Values[event.OFFSET]
		assert.False(t, hasOffset, "should not have offset")

		phase, hasPhase := e.Values[event.PHASE_STATUS]
		assert.True(t, hasPhase, "should have phase status")
		assert.Equal(t, int64(DPLL_LOCKED), phase)

	case <-time.After(1 * time.Second):
		t.Fatal("Timeout waiting for event")
	}
}

func TestPtpSettingsKeys(t *testing.T) {
	assert.Equal(t, "dpll.eth0.ignore", PtpSettingsDpllIgnoreKey("eth0"))
	assert.Equal(t, "dpll.eth1.flags", PtpSettingsDpllFlagsKey("eth1"))
}

func TestActivePhaseOffsetPin(t *testing.T) {
	const (
		testClockID   uint64 = 0xAABBCCDD
		otherClockID  uint64 = 0x11223344
		ppsDeviceID   uint32 = 10
		eecDeviceID   uint32 = 20
		otherDeviceID uint32 = 30
	)

	ppsDevice := &nl.DoDeviceGetReply{
		ID:      ppsDeviceID,
		ClockID: testClockID,
		Type:    nl.DpllTypePPS,
	}
	eecDevice := &nl.DoDeviceGetReply{
		ID:      eecDeviceID,
		ClockID: testClockID,
		Type:    nl.DpllTypeEEC,
	}
	otherClockPPS := &nl.DoDeviceGetReply{
		ID:      otherDeviceID,
		ClockID: otherClockID,
		Type:    nl.DpllTypePPS,
	}

	tests := []struct {
		name          string
		clockID       uint64
		devices       []*nl.DoDeviceGetReply
		pin           *nl.PinInfo
		expectedIndex int
		expectedOk    bool
	}{
		{
			name:    "pin clock ID mismatch",
			clockID: testClockID,
			devices: []*nl.DoDeviceGetReply{ppsDevice},
			pin: &nl.PinInfo{
				ClockID: otherClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentID: ppsDeviceID, State: nl.PinStateConnected},
				},
			},
			expectedIndex: -1,
			expectedOk:    false,
		},
		{
			name:    "connected to PPS device with matching clock",
			clockID: testClockID,
			devices: []*nl.DoDeviceGetReply{ppsDevice, eecDevice},
			pin: &nl.PinInfo{
				ClockID: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentID: ppsDeviceID, State: nl.PinStateConnected},
				},
			},
			expectedIndex: 0,
			expectedOk:    true,
		},
		{
			name:    "disconnected from PPS device",
			clockID: testClockID,
			devices: []*nl.DoDeviceGetReply{ppsDevice},
			pin: &nl.PinInfo{
				ClockID: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentID: ppsDeviceID, State: nl.PinStateDisconnected},
				},
			},
			expectedIndex: -1,
			expectedOk:    false,
		},
		{
			name:    "connected to EEC device only",
			clockID: testClockID,
			devices: []*nl.DoDeviceGetReply{eecDevice},
			pin: &nl.PinInfo{
				ClockID: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentID: eecDeviceID, State: nl.PinStateConnected},
				},
			},
			expectedIndex: -1,
			expectedOk:    false,
		},
		{
			name:    "connected to PPS device but different clock ID in device",
			clockID: testClockID,
			devices: []*nl.DoDeviceGetReply{otherClockPPS},
			pin: &nl.PinInfo{
				ClockID: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentID: otherDeviceID, State: nl.PinStateConnected},
				},
			},
			expectedIndex: -1,
			expectedOk:    false,
		},
		{
			name:    "multiple parents, second is connected PPS",
			clockID: testClockID,
			devices: []*nl.DoDeviceGetReply{ppsDevice, eecDevice},
			pin: &nl.PinInfo{
				ClockID: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentID: eecDeviceID, State: nl.PinStateConnected},
					{ParentID: ppsDeviceID, State: nl.PinStateConnected},
				},
			},
			expectedIndex: 1,
			expectedOk:    true,
		},
		{
			name:    "selectable state is not connected",
			clockID: testClockID,
			devices: []*nl.DoDeviceGetReply{ppsDevice},
			pin: &nl.PinInfo{
				ClockID: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentID: ppsDeviceID, State: nl.PinStateSelectable},
				},
			},
			expectedIndex: -1,
			expectedOk:    false,
		},
		{
			name:    "no parent devices",
			clockID: testClockID,
			devices: []*nl.DoDeviceGetReply{ppsDevice},
			pin: &nl.PinInfo{
				ClockID:      testClockID,
				ParentDevice: []nl.PinParentDevice{},
			},
			expectedIndex: -1,
			expectedOk:    false,
		},
		{
			name:    "no cached devices",
			clockID: testClockID,
			devices: nil,
			pin: &nl.PinInfo{
				ClockID: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentID: ppsDeviceID, State: nl.PinStateConnected},
				},
			},
			expectedIndex: -1,
			expectedOk:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &DpllConfig{
				clockId: tt.clockID,
				devices: tt.devices,
			}
			index, ok := d.ActivePhaseOffsetPin(tt.pin)
			assert.Equal(t, tt.expectedIndex, index, "device index")
			assert.Equal(t, tt.expectedOk, ok, "match result")
		})
	}
}
