package clock

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func osSample(source event.EventSource, offset int64, state event.PTPState) event.Event {
	return event.Event{Source: source, IFace: "CLOCK_REALTIME", Data: &event.OffsetData{Offset: offset, State: state}}
}

func TestOsClock_Hysteresis(t *testing.T) {
	o := OsClock{State: event.PTP_FREERUN}
	o.ConfigureFilter(OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 3})
	for i, tt := range []struct {
		offset int64
		want   event.PTPState
	}{
		{10, event.PTP_NOTSET}, {20, event.PTP_NOTSET}, {30, event.PTP_LOCKED},
		{120, event.PTP_LOCKED}, {40, event.PTP_LOCKED},
		{150, event.PTP_LOCKED}, {160, event.PTP_LOCKED}, {170, event.PTP_FREERUN},
		{75, event.PTP_FREERUN}, {75, event.PTP_FREERUN}, {75, event.PTP_FREERUN},
		{10, event.PTP_FREERUN}, {10, event.PTP_FREERUN}, {10, event.PTP_LOCKED},
	} {
		ev := osSample(event.PHC2SYS, tt.offset, event.PTP_NOTSET)
		assert.True(t, o.AddEvent(ev), "samples are handled even during debounce or without a transition")
		d := o.GetData(event.PHC2SYS)
		assert.Equal(t, tt.want, d.State, "sample %d", i)
		assert.Equal(t, event.PTP_NOTSET, ev.Data.(*event.OffsetData).State, "input must stay raw")
		assert.Nil(t, d.Window, "phc2sys must not keep an offset history")
		require.NotNil(t, d.GetDataDetails(ev.IFace))
		assert.Equal(t, tt.offset, d.GetDataDetails(ev.IFace).Offset)
		if tt.want != event.PTP_NOTSET {
			assert.Equal(t, tt.want, o.State)
		} else {
			assert.Equal(t, event.PTP_FREERUN, o.State, "debounce does not expose NOTSET externally")
		}
	}
}

func TestOsClock_HoldoverAndReset(t *testing.T) {
	o := OsClock{State: event.PTP_FREERUN}
	o.ConfigureFilter(OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 2})
	for i := 0; i < 2; i++ {
		o.AddEvent(osSample(event.PHC2SYS, 10, event.PTP_LOCKED))
	}
	require.Equal(t, event.PTP_LOCKED, o.State)
	o.AddEvent(osSample(event.PHC2SYS, 10, event.PTP_HOLDOVER))
	o.AddEvent(osSample(event.PHC2SYS, 10, event.PTP_HOLDOVER))
	assert.Equal(t, event.PTP_HOLDOVER, o.State)
	o.AddEvent(osSample(event.PHC2SYS, 10, event.PTP_LOCKED))
	assert.Equal(t, event.PTP_NOTSET, o.GetData(event.PHC2SYS).State)
	assert.Equal(t, event.PTP_HOLDOVER, o.State)
	o.AddEvent(osSample(event.PHC2SYS, 10, event.PTP_LOCKED))
	assert.Equal(t, event.PTP_LOCKED, o.State)

	o.E3State = event.PTP_LOCKED
	o.Reset()
	assert.Equal(t, event.PTP_FREERUN, o.State)
	assert.Equal(t, event.PTP_NOTSET, o.E3State)
	assert.Equal(t, "CLOCK_REALTIME", o.IFace)
	assert.Zero(t, o.Offset)
	assert.Empty(t, o.ProcessData())
	o.AddEvent(osSample(event.PHC2SYS, 10, event.PTP_LOCKED))
	assert.Equal(t, event.PTP_NOTSET, o.GetData(event.PHC2SYS).State, "reset restarts the sample streak")
	o.AddEvent(osSample(event.PHC2SYS, 10, event.PTP_LOCKED))
	assert.Equal(t, event.PTP_LOCKED, o.State, "reset retains configuration")
}

func TestOsClock_ChronydBypassesPhc2sysFilter(t *testing.T) {
	o := OsClock{State: event.PTP_FREERUN}
	o.ConfigureFilter(OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 100, Samples: 10})
	o.AddEvent(osSample(event.CHRONYD, 200, event.PTP_LOCKED))
	assert.Equal(t, event.PTP_LOCKED, o.State)
	assert.Equal(t, event.PTP_LOCKED, o.GetData(event.CHRONYD).State)
	assert.Equal(t, float64(200), o.GetData(event.CHRONYD).Window.LastInserted())
}

func TestOsClock_InvalidEventsDoNotUpdateSamples(t *testing.T) {
	o := OsClock{State: event.PTP_FREERUN, IFace: "CLOCK_REALTIME"}
	assert.False(t, o.AddEvent(osSample(event.PTP4l, 10, event.PTP_LOCKED)))
	assert.False(t, o.AddEvent(event.Event{Source: event.PHC2SYS, Data: &event.SelectedSourceData{IFace: "eth0"}}))
	assert.False(t, o.AddEvent(event.Event{Source: event.CHRONYD, Data: &event.StateData{State: event.PTP_LOCKED}}))
	assert.False(t, o.AddEvent(event.Event{Source: event.PHC2SYS, Data: (*event.OffsetData)(nil)}))
	assert.Empty(t, o.ProcessData())
	assert.Equal(t, "CLOCK_REALTIME", o.IFace)
	assert.Equal(t, event.PTP_FREERUN, o.State)
}
