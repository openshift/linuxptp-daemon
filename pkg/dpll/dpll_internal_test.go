package dpll

import (
	"testing"

	nl "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/stretchr/testify/assert"
)

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
