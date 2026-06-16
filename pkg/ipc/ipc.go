package ipc

import (
	"encoding/json"
	"io"
)

// Version is the IPC protocol version.
const Version = 1

// IPC message types.
const (
	TypePTPState          = "ptp_state"
	TypeOSClockState      = "os_clock_state"
	TypeClockClass        = "clock_class"
	TypeGNSSState         = "gnss_state"
	TypeSyncEState        = "synce_state"
	TypeSyncEClockQuality = "synce_clock_quality"
	TypeSyncState         = "sync_state"
	TypeCacheClear        = "cache_clear"
	TypeStatusRequest     = "status_request"
	TypeStatusResponse    = "status_response"
)

// Synchronization state values.
const (
	StateLocked   = "LOCKED"
	StateHoldover = "HOLDOVER"
	StateFreerun  = "FREERUN"
)

// GNSS synchronization state values.
const (
	GNSSSynchronized        = "SYNCHRONIZED"
	GNSSAcquiringSync       = "ACQUIRING_SYNC"
	GNSSAntennaDisconnected = "ANTENNA_DISCONNECTED"
	GNSSBooting             = "BOOTING"
	GNSSAntennaShortCircuit = "ANTENNA_SHORT_CIRCUIT"
	GNSSFailureMultipath    = "FAILURE_MULTIPATH"
	GNSSFailureNoFix        = "FAILURE_NOFIX"
	GNSSFailureLowSNR       = "FAILURE_LOW_SNR"
	GNSSFailurePLL          = "FAILURE_PLL"
)

// Value is an interface implemented by all IPC message value types.
type Value interface {
	Value()
}

// Message is the IPC message exchanged between the daemon and cloud-event-proxy.
type Message struct {
	Version   int    `json:"version"`
	Type      string `json:"type"`
	Timestamp string `json:"timestamp,omitempty"`
	Profile   string `json:"profile,omitempty"`
	IFace     string `json:"iface,omitempty"`
	Values    Value  `json:"values,omitempty"`
}

// UnmarshalJSON implements json.Unmarshaler, using the Type field to select the correct Value type.
func (m *Message) UnmarshalJSON(data []byte) error {
	type raw struct {
		Version   int             `json:"version"`
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp,omitempty"`
		Profile   string          `json:"profile,omitempty"`
		IFace     string          `json:"iface,omitempty"`
		Values    json.RawMessage `json:"values,omitempty"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	m.Version = r.Version
	m.Type = r.Type
	m.Timestamp = r.Timestamp
	m.Profile = r.Profile
	m.IFace = r.IFace

	if len(r.Values) == 0 {
		return nil
	}

	switch r.Type {
	case TypePTPState, TypeOSClockState:
		var v StateValue
		if err := json.Unmarshal(r.Values, &v); err != nil {
			return err
		}
		m.Values = v
	case TypeGNSSState:
		var v GNSSStateValue
		if err := json.Unmarshal(r.Values, &v); err != nil {
			return err
		}
		m.Values = v
	case TypeSyncState:
		var v SyncStateValue
		if err := json.Unmarshal(r.Values, &v); err != nil {
			return err
		}
		m.Values = v
	case TypeSyncEState:
		var v SyncEStateValue
		if err := json.Unmarshal(r.Values, &v); err != nil {
			return err
		}
		m.Values = v
	case TypeClockClass:
		var v ClockClassValue
		if err := json.Unmarshal(r.Values, &v); err != nil {
			return err
		}
		m.Values = v
	case TypeSyncEClockQuality:
		var v SyncEClockQualityValue
		if err := json.Unmarshal(r.Values, &v); err != nil {
			return err
		}
		m.Values = v
	}
	return nil
}

// StateValue carries a PTP or OS clock synchronization state.
type StateValue struct {
	State string `json:"state"`
}

// Value implements Value.
func (StateValue) Value() {}

// GNSSStateValue carries a GNSS synchronization state.
type GNSSStateValue struct {
	State string `json:"state"`
}

// Value implements Value.
func (GNSSStateValue) Value() {}

// SyncStateValue carries the overall sync state.
type SyncStateValue struct {
	State string `json:"state"`
}

// Value implements Value.
func (SyncStateValue) Value() {}

// SyncEStateValue carries a SyncE synchronization state.
type SyncEStateValue struct {
	State string `json:"state"`
}

// Value implements Value.
func (SyncEStateValue) Value() {}

// ClockClassValue carries a PTP clock class.
type ClockClassValue struct {
	ClockClass uint8 `json:"clock_class"`
}

// Value implements Value.
func (ClockClassValue) Value() {}

// SyncEClockQualityValue carries SyncE clock quality levels.
type SyncEClockQualityValue struct {
	QL         int `json:"ql"`
	ExtendedQL int `json:"extended_ql"`
}

// Value implements Value.
func (SyncEClockQualityValue) Value() {}

// Encode encodes the given msgs as newline deliminated JSON, and writes them to the given writer
func Encode(w io.Writer, msgs []Message) error {
	enc := json.NewEncoder(w)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			return err
		}
	}
	return nil
}
