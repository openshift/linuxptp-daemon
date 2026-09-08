package clockmgr

import (
	"context"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testClockmgrPtp4lConfig = "ptp4l.0.config"

// GetPTPState returns the stored PTP state for source under cfgName.
func (m *ClockManager) GetPTPState(source event.EventSource, cfgName string) event.PTPState {
	clk := m.GetClock(cfgName)
	if clk == nil {
		return event.PTP_UNKNOWN
	}
	d := clk.GetData(source)
	if d == nil {
		return event.PTP_UNKNOWN
	}
	return d.State
}

func TestClockManager_GetDataAndPTPState(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	oc, err := clock.NewBC(testClockmgrPtp4lConfig, true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(oc)
	require.NoError(t, err)

	d := cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC)
	require.NotNil(t, d)
	assert.Equal(t, event.PTP_UNKNOWN, cm.GetPTPState(event.TS2PHC, "ts2phc.0.config"))

	ev := event.Event{
		Source:  event.TS2PHC,
		CfgName: "ts2phc.0.config",
		IFace:   "eth0",
		Data: &event.OffsetData{
			State:  event.PTP_LOCKED,
			Offset: 100,
		},
	}
	d.AddEvent(ev)
	d.UpdateState()
	assert.Equal(t, event.PTP_LOCKED, cm.GetPTPState(event.TS2PHC, "ts2phc.0.config"))
	assert.Nil(t, cm.GetClock("missing.config"))
	assert.Equal(t, event.PTP_UNKNOWN, cm.GetPTPState(event.TS2PHC, "missing.config"))
}

func TestClockManager_EmitClockClassAndIPCCache(t *testing.T) {
	cache := ipc.NewCache(10)
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, cache)
	assert.Equal(t, cache, cm.ipcCache)
	bc, err := clock.NewBC(testClockmgrPtp4lConfig, false, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(bc)
	require.NoError(t, err)

	bc.AddEvent(event.Event{
		Source: event.PMC,
		Data:   &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6}},
	})
	require.Equal(t, fbprotocol.ClockClass(6), bc.ClockClass())

	found := false
	for _, msg := range cache.Snapshot() {
		if msg.Type == ipc.TypeClockClass {
			found = true
			assert.Equal(t, uint8(6), msg.Values.(ipc.ClockClassValue).ClockClass)
		}
	}
	assert.True(t, found, "clock class IPC should be cached after ParentDS update")
}

func TestClockManager_EmitClockClassAndIPCCach_OC(t *testing.T) {
	cache := ipc.NewCache(10)
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, cache)
	assert.Equal(t, cache, cm.ipcCache)
	bc, err := clock.NewBC(testClockmgrPtp4lConfig, true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(bc)
	require.NoError(t, err)

	bc.AddEvent(event.Event{
		Source: event.PMC,
		Data:   &event.ParentDSData{ParentDataSet: protocol.ParentDataSet{GrandmasterClockClass: 6}},
	})
	require.Equal(t, fbprotocol.ClockClass(255), bc.ClockClass())

	found := false
	for _, msg := range cache.Snapshot() {
		if msg.Type == ipc.TypeClockClass {
			found = true
			assert.Equal(t, uint8(255), msg.Values.(ipc.ClockClassValue).ClockClass)
		}
	}
	assert.True(t, found, "clock class IPC should be cached after ParentDS update")
}

func TestClockManager_GetWindowsPrefersMatchingProcess(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	bc1, err := clock.NewBC("ptp4l.0.config", true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(bc1)
	require.NoError(t, err)

	ptp4lEv := event.Event{
		Source:  event.PTP4l,
		CfgName: "ptp4l.0.config",
		IFace:   "eth0",
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 10},
	}
	ts2phcEv := event.Event{
		Source:  event.TS2PHC,
		CfgName: "ts2phc.0.config",
		IFace:   "eth0",
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 99},
	}
	// AddEvent only records the offset window after the iface already exists.
	cm.GetClock("ptp4l.0.config").GetData(event.PTP4l).AddEvent(ptp4lEv)
	cm.GetClock("ptp4l.0.config").GetData(event.PTP4l).AddEvent(ptp4lEv)
	cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC).AddEvent(ts2phcEv)
	cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC).AddEvent(ts2phcEv)

	windows := cm.GetWindows([]process.WindowRequest{{ClockID: "ptp4l.0.config", Source: event.PTP4l}, {ClockID: "ts2phc.0.config", Source: event.TS2PHC}})
	require.NotNil(t, windows["ptp4l.0.config"])
	require.NotNil(t, windows["ts2phc.0.config"])
	assert.Equal(t, float64(10), windows["ptp4l.0.config"][event.PTP4l].LastInserted())
	assert.Equal(t, float64(99), windows["ts2phc.0.config"][event.TS2PHC].LastInserted())
	assert.Nil(t, windows["missing.config"])
}

func TestClockManager_PHC2SYS_ReturnedViaProcessData(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	clk, err := clock.NewBC("ptp4l.0.config", true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(clk)
	require.NoError(t, err)

	phc2sysEv1 := event.Event{
		Source:  event.PHC2SYS,
		CfgName: "ptp4l.0.config",
		IFace:   "eth0",
		Data: &event.OffsetData{
			State:  event.PTP_LOCKED,
			Offset: 1234,
		},
	}
	phc2sysEv2 := event.Event{
		Source:  event.PHC2SYS,
		CfgName: "ptp4l.0.config",
		IFace:   "eth0",
		Data: &event.OffsetData{
			State:  event.PTP_LOCKED,
			Offset: 5678,
		},
	}

	cm.handleOSClockEvent(phc2sysEv1)
	cm.handleOSClockEvent(phc2sysEv2)

	dataList := clk.ProcessData()
	require.NotEmpty(t, dataList)

	var phc2sysData *event.Data
	for _, d := range dataList {
		if d.ProcessName == event.PHC2SYS {
			phc2sysData = d
			break
		}
	}
	require.NotNil(t, phc2sysData, "ProcessData from clock must include PHC2SYS data")
	assert.Equal(t, float64(5678), phc2sysData.Window.LastInserted())
	assert.Equal(t, event.PTP_LOCKED, phc2sysData.State)
}

func TestClockManager_PHC2SYS_ProcessEventsLoop(t *testing.T) {
	evCh := make(chan event.Event, 10)
	cm := Init("test-node", evCh, nil, nil, nil, nil)
	oc, err := clock.NewBC("ptp4l.0.config", true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(oc)
	require.NoError(t, err)

	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go cm.ProcessEvents(ctx)

	// Send initial event to register interface detail and second to insert sample into window
	for _, offset := range []int64{100, 200, 300} {
		evCh <- event.Event{
			Source:  event.PHC2SYS,
			CfgName: "ptp4l.0.config",
			IFace:   "eth0",
			Data: &event.OffsetData{
				State:  event.PTP_LOCKED,
				Offset: offset,
			},
		}
	}

	assert.Eventually(t, func() bool {
		for _, d := range oc.ProcessData() {
			if d.ProcessName == event.PHC2SYS && d.Window.LastInserted() == 300 {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "PHC2SYS event should be processed and reflected in clock ProcessData")
}

func TestClockManager_PHC2SYS_SharedAcrossMultipleClocks(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)

	clk1, err := clock.NewBC("ptp4l.0.config", true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(clk1)
	require.NoError(t, err)

	clk2, err := clock.NewBC("ptp4l.1.config", true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(clk2)
	require.NoError(t, err)

	clk3, err := clock.NewBC("ptp4l.2.config", true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(clk3)
	require.NoError(t, err)

	// Send PHC2SYS offset event
	for _, offset := range []int64{500, 1500} {
		cm.handleOSClockEvent(event.Event{
			Source:  event.PHC2SYS,
			CfgName: "ptp4l.0.config",
			IFace:   "eth0",
			Data: &event.OffsetData{
				State:  event.PTP_LOCKED,
				Offset: offset,
			},
		})
	}

	// Verify all clocks receive the shared PHC2SYS data via their ProcessData()
	for _, clk := range []clock.Clock{clk1, clk2, clk3} {
		var foundPHC2SYS bool
		for _, d := range clk.ProcessData() {
			if d.ProcessName == event.PHC2SYS {
				foundPHC2SYS = true
				assert.Equal(t, float64(1500), d.Window.LastInserted())
				assert.Equal(t, event.PTP_LOCKED, d.State)
			}
		}
		assert.True(t, foundPHC2SYS, "clock %s should contain PHC2SYS in ProcessData()", clk.ConfigName())
	}
}

func TestClockManager_PHC2SYS_GetWindowsQuery(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	clk, err := clock.NewBC("ptp4l.0.config", true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(clk)
	require.NoError(t, err)

	for _, offset := range []int64{25, 50} {
		cm.handleOSClockEvent(event.Event{
			Source:  event.PHC2SYS,
			CfgName: "ptp4l.0.config",
			IFace:   "eth0",
			Data: &event.OffsetData{
				State:  event.PTP_LOCKED,
				Offset: offset,
			},
		})
	}

	// Query via GetWindows
	windows := cm.GetWindows([]process.WindowRequest{
		{ClockID: "ptp4l.0.config", Source: event.PHC2SYS},
	})
	require.NotNil(t, windows["ptp4l.0.config"])
	require.NotNil(t, windows["ptp4l.0.config"][event.PHC2SYS])
	assert.Equal(t, float64(50), windows["ptp4l.0.config"][event.PHC2SYS].LastInserted())

	// Also verify directly from ProcessData
	var phc2sysData *event.Data
	for _, d := range clk.ProcessData() {
		if d.ProcessName == event.PHC2SYS {
			phc2sysData = d
			break
		}
	}
	require.NotNil(t, phc2sysData)
	assert.Equal(t, windows["ptp4l.0.config"][event.PHC2SYS], &phc2sysData.Window)
}

func TestClockManager_PHC2SYS_RemoveAllClocksResetsData(t *testing.T) {
	cm := Init("test-node", make(chan event.Event), nil, nil, nil, nil)
	clk, err := clock.NewBC("ptp4l.1.config", true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(clk)
	require.NoError(t, err)

	for _, offset := range []int64{100, 200} {
		cm.handleOSClockEvent(event.Event{
			Source:  event.PHC2SYS,
			CfgName: "ptp4l.0.config",
			IFace:   "eth0",
			Data: &event.OffsetData{
				State:  event.PTP_LOCKED,
				Offset: offset,
			},
		})
	}

	// Verify data is present
	require.NotEmpty(t, clk.ProcessData())

	// Reset via RemoveAllClocks
	cm.RemoveAllClocks()

	// Re-add clock and verify ProcessData has no stale PHC2SYS data
	newClk, err := clock.NewBC("ptp4l.1.config", true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(newClk)
	require.NoError(t, err)

	for _, d := range newClk.ProcessData() {
		assert.NotEqual(t, event.PHC2SYS, d.ProcessName, "stale PHC2SYS data must be cleared on Reset")
	}
}
