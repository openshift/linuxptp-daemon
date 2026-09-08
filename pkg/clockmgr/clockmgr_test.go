package clockmgr

import (
	"testing"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testClockmgrETH0        = "eth0"
	testClockmgrPtp4lConfig = "ptp4l.0.config"
)

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
		IFace:   testClockmgrETH0,
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
	bc1, err := clock.NewBC(testClockmgrPtp4lConfig, true, event.PtpClockThreshold{})
	require.NoError(t, err)
	err = cm.AddClock(bc1)
	require.NoError(t, err)

	ptp4lEv := event.Event{
		Source:  event.PTP4l,
		CfgName: testClockmgrPtp4lConfig,
		IFace:   testClockmgrETH0,
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 10},
	}
	ts2phcEv := event.Event{
		Source:  event.TS2PHC,
		CfgName: "ts2phc.0.config",
		IFace:   testClockmgrETH0,
		Data:    &event.OffsetData{State: event.PTP_LOCKED, Offset: 99},
	}
	// AddEvent only records the offset window after the iface already exists.
	cm.GetClock(testClockmgrPtp4lConfig).GetData(event.PTP4l).AddEvent(ptp4lEv)
	cm.GetClock(testClockmgrPtp4lConfig).GetData(event.PTP4l).AddEvent(ptp4lEv)
	cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC).AddEvent(ts2phcEv)
	cm.GetClock("ts2phc.0.config").GetData(event.TS2PHC).AddEvent(ts2phcEv)

	windows := cm.GetWindows([]process.WindowRequest{{ClockID: testClockmgrPtp4lConfig, Source: event.PTP4l}, {ClockID: "ts2phc.0.config", Source: event.TS2PHC}})
	require.NotNil(t, windows[testClockmgrPtp4lConfig])
	require.NotNil(t, windows["ts2phc.0.config"])
	assert.Equal(t, float64(10), windows[testClockmgrPtp4lConfig][event.PTP4l].LastInserted())
	assert.Equal(t, float64(99), windows["ts2phc.0.config"][event.TS2PHC].LastInserted())
	assert.Nil(t, windows["missing.config"])
}
