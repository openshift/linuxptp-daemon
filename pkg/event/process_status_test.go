package event

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessStatusEvent(t *testing.T) {
	ev := ProcessStatusEvent(PTP4l, "ptp4l.0.config", OC, "", 0)
	ps, ok := ev.Data.(*ProcessStatusData)
	require.True(t, ok)
	assert.Equal(t, int64(0), ps.Status)
	assert.False(t, ev.Reset)
	assert.True(t, ev.WriteToLog)
	assert.Contains(t, ev.GetLogData(), "PTP_PROCESS_STATUS:0")

	evUp := ProcessStatusEvent(TS2PHC, "ts2phc.0.config", GM, "", 1)
	ps, ok = evUp.Data.(*ProcessStatusData)
	require.True(t, ok)
	assert.Equal(t, int64(1), ps.Status)
	assert.Contains(t, evUp.GetLogData(), "PTP_PROCESS_STATUS:1")
}
