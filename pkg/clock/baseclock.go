package clock

import (
	"sync"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

// DataHolder manages process data slice with thread safety.
type DataHolder struct {
	data   []*event.Data
	dataMu sync.RWMutex
}

// GetData returns the Data entry for the given process, creating one if needed.
func (dh *DataHolder) GetData(processName event.EventSource) *event.Data {
	dh.dataMu.Lock()
	defer dh.dataMu.Unlock()
	for _, d := range dh.data {
		if d.ProcessName == processName {
			return d
		}
	}
	d := &event.Data{ProcessName: processName, State: event.PTP_UNKNOWN, Window: *utils.NewWindow(event.WindowSize)}
	dh.data = append(dh.data, d)
	return d
}

// Reset clears the stored data.
func (dh *DataHolder) Reset() {
	dh.dataMu.RLock()
	defer dh.dataMu.RUnlock()
	dh.data = nil
}

// ProcessData returns all clock data accumulated from processed events.
func (dh *DataHolder) ProcessData() []*event.Data {
	dh.dataMu.RLock()
	defer dh.dataMu.RUnlock()
	return dh.data
}

// BaseClock ...
type BaseClock struct {
	DataHolder
	sendIPC          func(ipc.Message)
	cfgName          string
	overallSyncState event.PTPState
	osClock          *OsClock
	threshold        event.PtpClockThreshold
}

// ConfigName ..
func (c *BaseClock) ConfigName() string { return c.cfgName }

// Reset resets the clock state.
func (c *BaseClock) Reset() {
	c.osClock.Reset()
	c.DataHolder.Reset()
	c.overallSyncState = event.PTP_FREERUN
}

// ProcessData returns all clock data accumulated from processed events.
func (c *BaseClock) ProcessData() []*event.Data {
	d := c.DataHolder.ProcessData()
	return append(d, c.osClock.ProcessData()...)
}

// SetOsClock Set OsClock
func (c *BaseClock) SetOsClock(osClock *OsClock) {
	c.osClock = osClock
}

// SetIPC sets the IPC sender function
func (c *BaseClock) SetIPC(f func(message ipc.Message)) {
	c.sendIPC = f
}

func newBaseClock(cfgName string, threshold event.PtpClockThreshold) BaseClock {
	return BaseClock{
		cfgName:          cfgName,
		threshold:        threshold,
		overallSyncState: event.PTP_NOTSET,
	}
}

// OsClock ...
type OsClock struct {
	DataHolder
	State event.PTPState
}

// Reset ...
func (o *OsClock) Reset() {
	o.State = event.PTP_FREERUN
	o.DataHolder.Reset()
}

// AddEvent processes OS clock events.
func (o *OsClock) AddEvent(ev event.Event) {
	if _, ok := ev.Data.(*event.OffsetData); !ok || (ev.Source != event.CHRONYD && ev.Source != event.PHC2SYS) {
		return
	}
	d := o.GetData(ev.Source)
	d.AddEvent(ev)
	d.UpdateState()
}
