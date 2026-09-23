package clock

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func stubUtcOffset() int { return 37 }

type ipcRecorder struct {
	messages []ipc.Message
}

func (r *ipcRecorder) send(msg ipc.Message) {
	r.messages = append(r.messages, msg)
}

// Generic event.Data tests

func TestAddEvent_StoresSpecFlags(t *testing.T) {
	t.Run("DPLL event stores outOfSpec and frequencyTraceable in DataDetails", func(t *testing.T) {
		d := &event.Data{ProcessName: event.DPLL, State: event.PTP_UNKNOWN, Window: *utils.NewWindow(event.WindowSize)}
		ev := event.Event{
			Source:    event.DPLL,
			IFace:     testTBCIface,
			ClockType: event.GM,
			Time:      0,
			Data:      &event.PTPData{State: event.PTP_FREERUN, OutOfSpec: true, FrequencyTraceable: true, Values: map[event.ValueType]interface{}{event.OFFSET: int64(0)}},
		}
		d.AddEvent(ev)

		dd := d.GetDataDetails("ens1f0")
		require.NotNil(t, dd)
		assert.True(t, dd.OutOfSpec)
		assert.True(t, dd.FrequencyTraceable)
	})

	t.Run("non-DPLL PTPData stores its own flags independently", func(t *testing.T) {
		dpll := &event.Data{ProcessName: event.DPLL, State: event.PTP_UNKNOWN, Window: *utils.NewWindow(event.WindowSize)}
		dpll.AddEvent(event.Event{
			Source: event.DPLL, IFace: "ens1f0", ClockType: event.GM, Time: 0,
			Data: &event.PTPData{State: event.PTP_FREERUN, OutOfSpec: true, FrequencyTraceable: true, Values: map[event.ValueType]interface{}{event.OFFSET: int64(0)}},
		})

		ts := &event.Data{ProcessName: event.TS2PHCProcessName, State: event.PTP_UNKNOWN, Window: *utils.NewWindow(event.WindowSize)}
		ts.AddEvent(event.Event{
			Source: event.TS2PHC, IFace: "ens1f0", ClockType: event.GM, Time: 0,
			Data: &event.PTPData{State: event.PTP_LOCKED, OutOfSpec: false, FrequencyTraceable: false, Values: map[event.ValueType]interface{}{event.OFFSET: int64(0)}},
		})

		dpllDD := dpll.GetDataDetails("ens1f0")
		require.NotNil(t, dpllDD)
		assert.True(t, dpllDD.OutOfSpec, "DPLL details should retain its flags")
		assert.True(t, dpllDD.FrequencyTraceable)

		tsDD := ts.GetDataDetails("ens1f0")
		require.NotNil(t, tsDD)
		assert.False(t, tsDD.OutOfSpec, "ts2phc details should have its own flags")
		assert.False(t, tsDD.FrequencyTraceable)
	})
}

func TestWorstOfState(t *testing.T) {
	tests := []struct {
		a, b     event.PTPState
		expected event.PTPState
	}{
		{event.PTP_LOCKED, event.PTP_LOCKED, event.PTP_LOCKED},
		{event.PTP_LOCKED, event.PTP_FREERUN, event.PTP_FREERUN},
		{event.PTP_FREERUN, event.PTP_LOCKED, event.PTP_FREERUN},
		{event.PTP_HOLDOVER, event.PTP_LOCKED, event.PTP_HOLDOVER},
		{event.PTP_LOCKED, event.PTP_HOLDOVER, event.PTP_HOLDOVER},
		{event.PTP_FREERUN, event.PTP_HOLDOVER, event.PTP_FREERUN},
		{event.PTP_HOLDOVER, event.PTP_FREERUN, event.PTP_FREERUN},
		{event.PTP_HOLDOVER, event.PTP_HOLDOVER, event.PTP_HOLDOVER},
		{event.PTP_FREERUN, event.PTP_FREERUN, event.PTP_FREERUN},
	}
	for _, tt := range tests {
		t.Run(string(tt.a)+"_"+string(tt.b), func(t *testing.T) {
			assert.Equal(t, tt.expected, worstOfState(tt.a, tt.b))
		})
	}
}
