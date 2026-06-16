package ipc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessageJSON(t *testing.T) {
	tests := []struct {
		name       string
		msg        Message
		wantValues Value
	}{
		{
			name: "state value",
			msg: Message{
				Version: Version, Type: TypePTPState,
				Timestamp: "2024-01-15T10:30:00.123456789Z",
				Profile:   "ptp4l.0.config", IFace: "ens2f0",
				Values: StateValue{State: StateLocked},
			},
			wantValues: StateValue{State: StateLocked},
		},
		{
			name: "gnss state value",
			msg: Message{
				Version: Version, Type: TypeGNSSState,
				Profile: "ts2phc.0.config", IFace: "ens2f0",
				Values: GNSSStateValue{State: GNSSSynchronized},
			},
			wantValues: GNSSStateValue{State: GNSSSynchronized},
		},
		{
			name: "sync state value",
			msg: Message{
				Version: Version, Type: TypeSyncState,
				Profile: "ptp4l.0.config",
				Values:  SyncStateValue{State: StateLocked},
			},
			wantValues: SyncStateValue{State: StateLocked},
		},
		{
			name: "synce state value",
			msg: Message{
				Version: Version, Type: TypeSyncEState,
				Profile: "synce4l.0.config", IFace: "ens7f0",
				Values: SyncEStateValue{State: StateLocked},
			},
			wantValues: SyncEStateValue{State: StateLocked},
		},
		{
			name: "clock class value",
			msg: Message{
				Version: Version, Type: TypeClockClass,
				Profile: "ptp4l.0.config",
				Values:  ClockClassValue{ClockClass: 6},
			},
			wantValues: ClockClassValue{ClockClass: 6},
		},
		{
			name: "synce clock quality value",
			msg: Message{
				Version: Version, Type: TypeSyncEClockQuality,
				Profile: "synce4l.0.config", IFace: "ens7f0",
				Values: SyncEClockQualityValue{QL: 2, ExtendedQL: 10},
			},
			wantValues: SyncEClockQualityValue{QL: 2, ExtendedQL: 10},
		},
		{
			name: "no values",
			msg: Message{
				Version: Version, Type: TypeCacheClear,
			},
			wantValues: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.msg)
			require.NoError(t, err)

			var got Message
			require.NoError(t, json.Unmarshal(data, &got))

			assert.Equal(t, tt.msg.Version, got.Version)
			assert.Equal(t, tt.msg.Type, got.Type)
			assert.Equal(t, tt.msg.Profile, got.Profile)
			assert.Equal(t, tt.msg.IFace, got.IFace)
			assert.Equal(t, tt.wantValues, got.Values)
		})
	}
}
