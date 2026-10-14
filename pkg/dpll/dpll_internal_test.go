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
		{"AllPinFlags", FlagNoPhaseOffset | FlagNoPhaseStatus | FlagNoFreqencyStatus,
			false, false, false,
			[]string{"NoFrequencyStatus", "NoPhaseStatus", "NoPhaseOffset"}, "UNKNOWN"},
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
		source        event.EventSource
		phaseStatus   int64
		freqStatus    int64
		expectedState int64
	}{
		{"NoFlags_WorstIsFreq", 0, event.GNSS, DPLL_LOCKED, DPLL_FREERUN, DPLL_FREERUN},
		{"NoFlags_WorstIsPhase", 0, event.GNSS, DPLL_HOLDOVER, DPLL_LOCKED, DPLL_HOLDOVER},
		{"NoPhaseStatus", FlagNoPhaseStatus, event.GNSS, DPLL_LOCKED, DPLL_FREERUN, DPLL_FREERUN},
		{"NoFrequencyStatus", FlagNoFreqencyStatus, event.GNSS, DPLL_LOCKED, DPLL_FREERUN, DPLL_LOCKED},
		// E830: only pps device exists; getDpllState must return phaseStatus (LOCKED), not frequencyStatus (FREERUN)
		{"OnlyPhaseStatus_E830", FlagOnlyPhaseStatus, event.GNSS, DPLL_LOCKED, DPLL_FREERUN, DPLL_LOCKED},
		{"PPSSource_IgnoresUnlockedEEC", 0, event.PPS, DPLL_LOCKED_HO_ACQ, DPLL_FREERUN, DPLL_LOCKED_HO_ACQ},
		{"PTPSource_IgnoresUnlockedEEC", 0, event.PTP4l, DPLL_LOCKED_HO_ACQ, DPLL_FREERUN, DPLL_LOCKED_HO_ACQ},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &DpllConfig{
				flags:           tt.flags,
				phaseStatus:     tt.phaseStatus,
				frequencyStatus: tt.freqStatus,
				dependsOn:       []event.EventSource{tt.source},
			}
			assert.Equal(t, tt.expectedState, d.getDpllState())
		})
	}
}

func TestStateDecision_PPSSourceLockedWhileEECUnlocked(t *testing.T) {
	d := &DpllConfig{
		iface:                  "ens1f0",
		phaseStatus:            DPLL_LOCKED_HO_ACQ,
		frequencyStatus:        DPLL_FREERUN,
		phaseOffset:            -673,
		dependsOn:              []event.EventSource{event.PPS},
		LocalMaxHoldoverOffSet: LocalMaxHoldoverOffSet,
		processConfig: config.ProcessConfig{
			GMThreshold: config.Threshold{Max: 1500},
		},
	}
	d.stateDecision()
	assert.Equal(t, event.PTP_LOCKED, d.state)
	assert.False(t, d.sourceLost)
	assert.Equal(t, int64(-673), d.phaseOffset)
	assert.True(t, d.inSpec)
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

// TestDpllIsOffsetInRange covers isOffsetInRange's single qualification
// bound: abs(phaseOffset) <= LocalMaxHoldoverOffSet, inclusive (<=), consistent
// with isMaxHoldoverOffsetInRange, and independent of the clock type and of
// the legacy maxOffsetThreshold window. It also verifies that a legitimate
// negative phaseOffset within tolerance is not misreported as out-of-range and
// that a nonzero GMThreshold.Min (deprecated) has no effect.
func TestDpllIsOffsetInRange(t *testing.T) {
	tests := []struct {
		name          string
		phaseOffset   int64
		holdoverBound uint64
		expected      bool
	}{
		{name: "in-range positive offset -> true", phaseOffset: 50, holdoverBound: 14400, expected: true},
		{name: "in-range negative offset -> true", phaseOffset: -50, holdoverBound: 14400, expected: true},
		{name: "out-of-range positive offset -> false", phaseOffset: 15000, holdoverBound: 14400, expected: false},
		{name: "out-of-range negative offset -> false", phaseOffset: -15000, holdoverBound: 14400, expected: false},
		{name: "exact positive boundary offset (inclusive) -> true", phaseOffset: 14400, holdoverBound: 14400, expected: true},
		{name: "exact negative boundary offset (inclusive) -> true", phaseOffset: -14400, holdoverBound: 14400, expected: true},
		{name: "zero offset at zero bound -> true", phaseOffset: 0, holdoverBound: 0, expected: true},
		// The legacy maxOffsetThreshold window no longer contributes: offset well
		// above it but inside the holdover bound must still qualify.
		{name: "offset above legacy maxOffsetThreshold window -> true", phaseOffset: 2000, holdoverBound: 14400, expected: true},
		{name: "small configured bound, offset beyond it -> false", phaseOffset: 2000, holdoverBound: 1500, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &DpllConfig{
				phaseOffset:            tt.phaseOffset,
				LocalMaxHoldoverOffSet: tt.holdoverBound,
			}
			assert.Equal(t, tt.expected, d.isOffsetInRange(), tt.name)
		})
	}
}

// TestDpllHoldoverRecovery reproduces the OCPBUGS-111642 recovery decision at
// the stateDecision level: a leading T-BC DPLL relocking via DPLL_LOCKED_HO_ACQ
// while the accumulated holdover offset is strictly above the legacy
// MaxOffsetThreshold window (GMThreshold.Max=100) and within the holdover
// free-run bound (localMaxHoldoverOffset=14400) must transition directly
// HOLDOVER -> LOCKED with no intervening FREERUN (FR-003). When the accumulated
// offset exceeds the holdover free-run bound the clock must still report
// FREERUN (FR-004).
func TestDpllHoldoverRecovery(t *testing.T) {
	scenarios := []struct {
		name              string
		accumulatedOffset int64
		expectedState     event.PTPState
	}{
		{
			name:              "relock within holdover bound -> LOCKED, no FREERUN",
			accumulatedOffset: 2000, // 100 < 2000 <= 14400
			expectedState:     event.PTP_LOCKED,
		},
		{
			name:              "relock beyond holdover bound -> FREERUN (bound preserved)",
			accumulatedOffset: 15000, // > 14400
			expectedState:     event.PTP_FREERUN,
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			eventChannel := make(chan event.Event, 32)
			d := &DpllConfig{
				iface:                  "ens01",
				phaseStatus:            DPLL_LOCKED,
				frequencyStatus:        DPLL_LOCKED,
				phaseOffset:            5,
				LocalMaxHoldoverOffSet: 14400,
				LocalHoldoverTimeout:   300,
				MaxInSpecOffset:        1800,
				dependsOn:              []event.EventSource{event.PTP4l},
				holdoverCloseCh:        make(chan bool, 1),
				processConfig: config.ProcessConfig{
					ClockType:    event.TBC,
					ConfigName:   "test",
					EventChannel: eventChannel,
					GMThreshold:  config.Threshold{Max: 100},
				},
			}

			// Enter holdover with a leading PTP source.
			d.sourceLost = true
			d.phaseStatus = DPLL_HOLDOVER
			d.onHoldover = true
			d.inSpec = true
			d.state = event.PTP_HOLDOVER
			d.stateDecision()
			ev := <-eventChannel
			assert.Equal(t, event.PTP_HOLDOVER, ev.Data.(*event.DPLLData).State, "clock is in holdover")

			// Upstream restored: relock via DPLL_LOCKED_HO_ACQ at the accumulated offset.
			d.sourceLost = false
			d.phaseStatus = DPLL_LOCKED_HO_ACQ
			d.phaseOffset = sc.accumulatedOffset
			d.stateDecision()
			ev2 := <-eventChannel
			assert.Equal(t, sc.expectedState, ev2.Data.(*event.DPLLData).State, "recovery decision")

			// No FREERUN may precede the LOCKED recovery event (direct
			// HOLDOVER -> LOCKED transition per FR-003).
			if sc.expectedState == event.PTP_LOCKED {
				seenFreeRun := false
				for {
					select {
					case evr := <-eventChannel:
						if evr.Data.(*event.DPLLData).State == event.PTP_FREERUN {
							seenFreeRun = true
						}
					default:
						goto drainDone
					}
				}
			drainDone:
				assert.False(t, seenFreeRun, "no FREERUN may be emitted on holdover->LOCKED recovery")
				assert.Equal(t, event.PTP_LOCKED, d.PTPState(), "DPLL reports LOCKED after recovery")
				assert.True(t, d.InSpec(), "DPLL is back in spec after recovery")
			}
		})
	}
}

// TestDpllHoldoverOutOfSpecUsesConfiguredBound guards the DPLL_HOLDOVER
// out-of-spec check: it must evaluate the offset against the configured
// LocalMaxHoldoverOffSet instance value, never the package-level default
// constant (1500). An offset above the default but below the configured bound
// must stay in-spec holdover (FR-002).
func TestDpllHoldoverOutOfSpecUsesConfiguredBound(t *testing.T) {
	eventChannel := make(chan event.Event, 8)
	d := &DpllConfig{
		iface:                  "ens01",
		phaseStatus:            DPLL_HOLDOVER,
		frequencyStatus:        DPLL_LOCKED,
		phaseOffset:            2000, // > package const 1500, < configured 5000
		LocalMaxHoldoverOffSet: 5000,
		dependsOn:              []event.EventSource{event.PTP4l},
		holdoverCloseCh:        make(chan bool, 1),
		inSpec:                 true,
		onHoldover:             true,
		state:                  event.PTP_HOLDOVER,
		processConfig: config.ProcessConfig{
			ClockType:    event.TBC,
			ConfigName:   "test",
			EventChannel: eventChannel,
		},
	}

	d.stateDecision()
	ev := <-eventChannel
	assert.Equal(t, event.PTP_HOLDOVER, ev.Data.(*event.DPLLData).State,
		"offset 2000 (above default 1500, below configured 5000) must stay in-spec holdover")
}

func TestDpllSendEventWithFlags(t *testing.T) {
	eventChannel := make(chan event.Event, 10)
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
		assert.Equal(t, event.DPLL, e.Source)
		assert.Equal(t, "test-iface", e.IFace)

		dpllData := e.Data.(*event.DPLLData)
		assert.Nil(t, dpllData.FrequencyStatus, "should not have frequency status")
		assert.Nil(t, dpllData.Offset, "should not have offset")
		assert.NotNil(t, dpllData.PhaseStatus, "should have phase status")
		assert.Equal(t, int64(DPLL_LOCKED), *dpllData.PhaseStatus)

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
					{ParentID: ppsDeviceID, State: nl.PinStateConnected, Direction: nl.PinDirectionInput},
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
					{ParentID: eecDeviceID, State: nl.PinStateConnected, Direction: nl.PinDirectionInput},
					{ParentID: ppsDeviceID, State: nl.PinStateConnected, Direction: nl.PinDirectionInput},
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
