package dpll

import (
	"context"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func waitDpllState(t *testing.T, d *DpllConfig, want process.State) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d.State() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for state %v, got %v", want, d.State())
}

func waitDpllStatus(t *testing.T, ch <-chan event.Event, want int64) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ps, ok := ev.Data.(*event.ProcessStatusData); ok && ps.Status == want {
				return
			}
		case <-deadline:
			t.Fatalf("timeout waiting for PROCESS_STATUS %d", want)
		}
	}
}

func TestDpllStartGates(t *testing.T) {
	d := NewDpll(1, 10, 2, 5, "ens01", []event.EventSource{event.GNSS}, MOCK, map[string]map[string]string{}, 0, 0, 0, config.ProcessConfig{}, &ptpv1.PtpProfile{})
	d.processState = process.Starting
	require.NoError(t, d.Start(context.Background()))
	assert.Equal(t, process.Starting, d.State())

	d.processState = process.Running
	require.NoError(t, d.Start(context.Background()))
	assert.Equal(t, process.Running, d.State())

	d.processState = process.Stopping
	err := d.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is stopping")
}

func TestDpllStartStopRestart(t *testing.T) {
	ch := make(chan event.Event, 8)
	d := NewDpll(1, 10, 2, 5, "ens01", []event.EventSource{event.GNSS}, MOCK, map[string]map[string]string{}, 0, 0, 0, config.ProcessConfig{
		ClockType:    event.GM,
		ConfigName:   "test",
		EventChannel: ch,
	}, &ptpv1.PtpProfile{})

	require.NoError(t, d.Start(context.Background()))
	waitDpllState(t, d, process.Running)
	waitDpllStatus(t, ch, 1)

	firstStopCh := d.stopCh
	require.NoError(t, d.Stop())
	waitDpllState(t, d, process.Stopped)
	waitDpllStatus(t, ch, 0)

	require.NoError(t, d.Stop())
	assert.Equal(t, process.Stopped, d.State())

	require.NoError(t, d.Start(context.Background()))
	waitDpllState(t, d, process.Running)
	waitDpllStatus(t, ch, 1)
	assert.NotEqual(t, firstStopCh, d.stopCh)
	select {
	case <-d.stopCh:
		t.Fatal("stopCh should be open after Start")
	default:
	}

	require.NoError(t, d.Stop())
	waitDpllState(t, d, process.Stopped)
}

func TestDpllStopCreatedNoop(t *testing.T) {
	d := NewDpll(1, 10, 2, 5, "ens01", []event.EventSource{event.GNSS}, MOCK, map[string]map[string]string{}, 0, 0, 0, config.ProcessConfig{}, &ptpv1.PtpProfile{})
	require.NoError(t, d.Stop())
	assert.Equal(t, process.Created, d.State())
}
