package clockmgr

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/debug"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	nodeLabel    = "node"
	processLabel = "process"
)

// ClockManager ... event handler to process events
type ClockManager struct {
	clockManagementMu sync.Mutex
	nodeName          string
	events            chan event.Event
	metricCache       map[metricKey]metricEntry
	registeredGauges  map[string]*prometheus.GaugeVec
	offsetMetric      *prometheus.GaugeVec
	clockMetric       *prometheus.GaugeVec
	clockClassMetric  *prometheus.GaugeVec
	portRole          map[string]map[string]*parser.PTPEvent
	clocks            map[string]clock.Clock // cfgName → Clock
	osClockState      event.PTPState
	ipcCache          *ipc.Cache
	// applyingProfiles is set while applyNodePTPProfiles is tearing down /
	// restarting processes. When true, T-BC/T-TSC events are skipped so
	// teardown races cannot emit spurious state transitions.
	applyingProfiles atomic.Bool
	// syncStatusCh is signalled (non-blocking) whenever a clock state transitions.
	syncStatusCh chan struct{}
}

type metricKey struct {
	cfgName  string
	process  string
	iface    string
	dataType event.ValueType
}

type metricEntry struct {
	gauge  *prometheus.GaugeVec
	labels prometheus.Labels
}

// SetApplying marks whether a PTP profile apply is in progress.
func (m *ClockManager) SetApplying(v bool) {
	m.applyingProfiles.Store(v)
}

// IsApplying reports whether a PTP profile apply is in progress.
func (m *ClockManager) IsApplying() bool {
	return m.applyingProfiles.Load()
}

// SyncStatusUpdateCh returns a channel that receives a signal (non-blocking send)
// whenever a clock state transitions. Missed signals are coalesced.
func (m *ClockManager) SyncStatusUpdateCh() <-chan struct{} {
	return m.syncStatusCh
}

func (m *ClockManager) signalSyncStatus() {
	select {
	case m.syncStatusCh <- struct{}{}:
	default:
	}
}

// Init ... initialize event manager
func Init(nodeName string, processChannel chan event.Event, offsetMetric *prometheus.GaugeVec, clockMetric *prometheus.GaugeVec, clockClassMetric *prometheus.GaugeVec, ipcCache *ipc.Cache) *ClockManager {
	return &ClockManager{
		nodeName:         nodeName,
		events:           processChannel,
		metricCache:      map[metricKey]metricEntry{},
		registeredGauges: map[string]*prometheus.GaugeVec{},
		clockMetric:      clockMetric,
		offsetMetric:     offsetMetric,
		clockClassMetric: clockClassMetric,
		portRole:         map[string]map[string]*parser.PTPEvent{},
		clocks:           map[string]clock.Clock{},
		osClockState:     event.PTP_NOTSET,
		ipcCache:         ipcCache,
		syncStatusCh:     make(chan struct{}, 1),
	}
}

// AddClock creates a Clock for the given config and registers it.
// If a clock is already registered for cfgName it is replaced.
// pmcClient may be nil for clock types that do not use PMC (e.g. BC, OC).
func (m *ClockManager) AddClock(cfgName string, clockType event.ClockType, pmcClient pmc.Client) (clock.Clock, error) {
	clk, err := clock.NewClock(cfgName, clockType, m.sendIPC, m.sendEvent, m.GetUtcOffset, pmcClient)
	if err != nil {
		return nil, err
	}
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	if prev, exists := m.clocks[cfgName]; exists {
		glog.Warningf("AddClock: replacing existing %s clock for config %s", prev.ClockType(), cfgName)
	}
	m.clocks[cfgName] = clk
	// BC/OC events may arrive with ts2phc.{runID}.config as cfgName,
	// so register under that key too.
	if clockType == event.BC || clockType == event.TBC || clockType == event.OC || clockType == event.GM {
		ts2phcName := strings.Replace(cfgName, "ptp4l.", "ts2phc.", 1)
		m.clocks[ts2phcName] = clk
	}
	glog.Infof("AddClock: registered %s clock for config %s", clockType, cfgName)
	return clk, nil
}

// RemoveAllClocks tears down all registered clocks and cleans up associated state.
func (m *ClockManager) RemoveAllClocks() {
	m.clockManagementMu.Lock()
	for cfgName := range m.clocks {
		delete(m.portRole, cfgName)
	}
	m.osClockState = event.PTP_NOTSET

	for cfgName := range m.clocks {
		m.unregisterMetrics(cfgName, "")
	}
	m.clocks = map[string]clock.Clock{}
	m.clockManagementMu.Unlock()

	debug.ClearState()
}

// GetClock returns the Clock registered for the given config name, or nil.
func (m *ClockManager) GetClock(cfgName string) clock.Clock {
	return m.clocks[cfgName]
}

// sendIPC sends an IPC message
func (m *ClockManager) sendIPC(msg ipc.Message) {
	if m.ipcCache != nil {
		m.ipcCache.Send(msg)
	}
}

// sendEvent posts an event back onto the processing channel so it is handled by
// the serialized ProcessEvents loop. It is used by clocks to feed asynchronous
// PMC results (e.g. the T-BC downstream announce) back to their state machines
// without mutating clock state off-loop. Callers must invoke it from a
// goroutine, never from the loop itself, to avoid blocking on a full channel.
func (m *ClockManager) sendEvent(ev event.Event) {
	m.events <- ev
}

// GetUtcOffset returns the current UTC offset.
func (m *ClockManager) GetUtcOffset() int {
	return leap.GetUtcOffset()
}

// handleOSClockEvent fans out a PHC2SYS/CHRONYD event to all clocks, emits os_clock_state message once, and emits
// a sync_state message per-profile when the overall state changes.
func (m *ClockManager) handleOSClockEvent(ev event.Event) {
	prevClockState := m.osClockState
	ptp, ok := ev.Data.(*event.PTPData)
	if !ok {
		glog.Warningf("handleOSClockEvent: received unexpected event")
		return
	}
	m.osClockState = ptp.State
	if m.osClockState == prevClockState {
		return
	}
	m.signalSyncStatus()

	// if the OS clock state changed, emit the event to CEP and also pass it along to each clock
	var osOffset int64
	if v, exists := ptp.Values[event.OFFSET]; exists {
		if i, isInt := v.(int64); isInt {
			osOffset = i
		}
	}
	m.sendIPC(ipc.Message{
		Type:   ipc.TypeOSClockState,
		IFace:  ev.IFace,
		Values: ipc.StateValue{State: event.PtpStateToIPCState(m.osClockState), Offset: osOffset},
	})
	for _, clk := range m.clocks {
		clk.SystemClockUpdate(m.osClockState)
	}
}

// ProcessEvents loops until
func (m *ClockManager) ProcessEvents(ctx context.Context) {
	glog.Info("starting state monitoring...")
	for {
		select {
		case ev, ok := <-m.events:
			if !ok {
				return
			}
			// TODO: This is a pretty large lock. Using it for simplicity. We should evaluate this in the future.
			//       I think a combination of the manager lock + locks for each individual clock will be the end goal.
			m.clockManagementMu.Lock()
			glog.V(14).Infof("ProcessEvents: received event source=%s iface=%s cfg=%s clockType=%s reset=%v",
				ev.Source, ev.IFace, ev.CfgName, ev.ClockType, ev.Reset)
			if ev.Reset {
				m.reset(ev)
				m.clockManagementMu.Unlock()
				continue
			}

			if ev.Source == event.PHC2SYS || ev.Source == event.CHRONYD {
				m.handleOSClockEvent(ev)
				m.clockManagementMu.Unlock()
				continue
			}

			// TODO: Move to better identifiers? Having to do this translation here is odd
			lookupName := ev.CfgName
			if ev.Source == event.SYNCE {
				lookupName = strings.Replace(ev.CfgName, "synce4l", "ptp4l", 1)
			}

			clk := m.GetClock(lookupName)
			if clk == nil {
				glog.Warningf("ProcessEvents: no clock registered for %s, skipping event", lookupName)
				m.clockManagementMu.Unlock()
				continue
			}

			if clk.ClockType() == event.TBC && m.IsApplying() {
				m.clockManagementMu.Unlock()
				continue
			}

			if ev.WriteToLog {
				if logData := ev.GetLogData(); logData != "" {
					fmt.Printf("%s", logData)
				}
			}
			prevState := clk.GetState()
			clockState := clk.AddEvent(ev)
			if prevState != event.PTP_NOTSET && prevState != clockState.State {
				m.signalSyncStatus()
			}
			if clockState.LeadingIFace != event.LEADING_INTERFACE_UNKNOWN {
				m.updateClockStateMetrics(clockState.State, string(ev.ClockType), alias.GetAlias(clockState.LeadingIFace))
			}
			m.updateMetrics(ev)
			m.clockManagementMu.Unlock()

		case <-ctx.Done():
			return
		}
	}
}

// reset handles a reset event
func (m *ClockManager) reset(ev event.Event) {
	debug.ClearState()
	if m.ipcCache != nil {
		m.ipcCache.Clear()
	}
	if clk := m.GetClock(ev.CfgName); clk != nil {
		clk.Reset()
	}
	if ev.Source == event.TS2PHC {
		m.unregisterMetrics(ev.CfgName, "")
	} else {
		m.unregisterMetrics(ev.CfgName, string(ev.Source))
	}
}

// updateClockStateMetrics should be used to update metrics when a clock changes state
func (m *ClockManager) updateClockStateMetrics(state event.PTPState, process, iFace string) {
	if m.clockMetric == nil {
		return
	}
	if !utils.CheckMetricSanity("ClockState", process, iFace) {
		return
	}
	labels := prometheus.Labels{
		"process": process, nodeLabel: m.nodeName, "iface": iFace}
	switch state {
	case event.PTP_LOCKED:
		m.clockMetric.With(labels).Set(event.ClockStateLocked)
	case event.PTP_HOLDOVER:
		m.clockMetric.With(labels).Set(event.ClockStateHoldover)
	default:
		m.clockMetric.With(labels).Set(event.ClockStateFreerun)
	}
}

// updateMetrics extracts numeric values from PTP events and updates Prometheus metrics.
// Metrics are cached by (cfgName, process, iface, dataType) to avoid re-registering.
func (m *ClockManager) updateMetrics(ev event.Event) {
	if m.offsetMetric == nil {
		return
	}
	iface := alias.GetAlias(ev.IFace)

	var processData map[event.ValueType]interface{}
	switch data := ev.Data.(type) {
	case *event.GNSSData:
		processData = map[event.ValueType]interface{}{
			event.GPS_STATUS: data.GPSStatus,
			event.OFFSET:     data.Offset,
		}
	case *event.PTPData:
		processData = data.Values
	default:
		return
	}

	for dataType, value := range processData {
		var dataValue float64
		switch val := value.(type) {
		case int64:
			dataValue = float64(val)
		case float64:
			dataValue = val
		default:
			continue
		}

		pName := string(ev.Source)
		if dataType == event.OFFSET && ev.Source == event.TS2PHCProcessName {
			pName = "master"
		}

		key := metricKey{
			cfgName:  ev.CfgName,
			process:  string(ev.Source),
			iface:    iface,
			dataType: dataType,
		}

		labels := prometheus.Labels{"from": pName, nodeLabel: m.nodeName,
			"process": string(ev.Source), "iface": iface}

		if entry, found := m.metricCache[key]; found {
			entry.labels = labels
			entry.gauge.With(labels).Set(dataValue)
			m.metricCache[key] = entry
		} else {
			var gauge *prometheus.GaugeVec
			metricName := getMetricName(dataType)

			if dataType == event.OFFSET {
				gauge = m.offsetMetric
			} else if existing, ok := m.registeredGauges[metricName]; ok {
				gauge = existing
			} else {
				gauge = prometheus.NewGaugeVec(
					prometheus.GaugeOpts{
						Namespace: event.PTPNamespace,
						Subsystem: event.PTPSubsystem,
						Name:      metricName,
						Help:      event.ValueTypeHelpTxt[dataType],
					}, []string{"from", "node", "process", "iface"})
				glog.Infof("trying to register metrics %s for %s", metricName, dataType)
				registerMetrics(gauge)
				m.registeredGauges[metricName] = gauge
			}

			gauge.With(labels).Set(dataValue)
			m.metricCache[key] = metricEntry{gauge: gauge, labels: labels}
		}
	}
}

func registerMetrics(m *prometheus.GaugeVec) {
	defer func() {
		if err := recover(); err != nil {
			glog.Errorf("restored from registering metrics: %s", err)
		}
	}()
	prometheus.MustRegister(m)
}

func (m *ClockManager) unregisterMetrics(configName string, processName string) {
	for key, entry := range m.metricCache {
		if key.cfgName == configName && (processName == "" || key.process == processName) {
			if entry.gauge != nil {
				entry.gauge.Delete(entry.labels)
			}
			delete(m.metricCache, key)
		}
	}
}

func getMetricName(valueType event.ValueType) string {
	if strings.HasSuffix(string(valueType), string(event.OFFSET)) {
		return fmt.Sprintf("%s_%s", valueType, "ns")
	}
	return string(valueType)
}
