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
		Id:      ppsDeviceID,
		ClockId: testClockID,
		Type:    nl.DpllTypePPS,
	}
	eecDevice := &nl.DoDeviceGetReply{
		Id:      eecDeviceID,
		ClockId: testClockID,
		Type:    nl.DpllTypeEEC,
	}
	otherClockPPS := &nl.DoDeviceGetReply{
		Id:      otherDeviceID,
		ClockId: otherClockID,
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
				ClockId: otherClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentId: ppsDeviceID, State: nl.PinStateConnected},
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
				ClockId: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentId: ppsDeviceID, State: nl.PinStateConnected},
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
				ClockId: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentId: ppsDeviceID, State: nl.PinStateDisconnected},
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
				ClockId: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentId: eecDeviceID, State: nl.PinStateConnected},
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
				ClockId: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentId: otherDeviceID, State: nl.PinStateConnected},
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
				ClockId: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentId: eecDeviceID, State: nl.PinStateConnected},
					{ParentId: ppsDeviceID, State: nl.PinStateConnected},
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
				ClockId: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentId: ppsDeviceID, State: nl.PinStateSelectable},
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
				ClockId:      testClockID,
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
				ClockId: testClockID,
				ParentDevice: []nl.PinParentDevice{
					{ParentId: ppsDeviceID, State: nl.PinStateConnected},
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
