package clockmgr

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/debug"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	configLabel  = "config"
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
	haMetric          *prometheus.GaugeVec
	clocks            map[string]clock.Clock // cfgName → Clock
	osClock           clock.OsClock
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

// SetHAMetric injects the openshift_ptp_ha_profile_status gauge. It is set
// separately from Init because the gauge is created and registered by the daemon
// package, which clockmgr cannot import.
func (m *ClockManager) SetHAMetric(g *prometheus.GaugeVec) {
	m.haMetric = g
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
		clocks:           map[string]clock.Clock{},
		osClock:          clock.OsClock{State: event.PTP_FREERUN},
		ipcCache:         ipcCache,
		syncStatusCh:     make(chan struct{}, 1),
	}
}

// AddClock takes ownership of a given Clock
// pmcClient may be nil for clock types that do not use PMC (e.g. BC, OC).
func (m *ClockManager) AddClock(clk clock.Clock) error {
	clk.SetIPC(m.sendIPC)
	clk.SetEventLoopbackFunc(m.sendEvent)
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	clk.SetOsClock(&m.osClock)
	// Register the clock under every config name its events may arrive under. For a
	// ptp4l-based clock this includes the ts2phc alias; for a composite HA clock it
	// includes each member's config.
	for _, cfg := range clk.ConfigNames() {
		if prev, exists := m.clocks[cfg]; exists {
			glog.Warningf("AddClock: replacing existing %s clock for config %s", prev.ClockType(), cfg)
		}
		m.clocks[cfg] = clk
	}
	glog.Infof("AddClock: registered %s clock for config %s", clk.ClockType(), clk.ConfigName())
	return nil
}

// GetWindows returns offset sample windows keyed by clock config name.
// If requiredStatsConfigs is empty, all windows are returned.
// Otherwise, only windows for configs in requiredStatsConfigs are returned.
func (m *ClockManager) GetWindows(windowRequests []process.WindowRequest) map[string]map[event.EventSource]utils.ROWindow {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()

	out := make(map[string]map[event.EventSource]utils.ROWindow)
	for _, req := range windowRequests {
		clk := m.GetClock(req.ClockID)
		if clk == nil {
			continue
		}
		if _, ok := out[req.ClockID]; !ok {
			out[req.ClockID] = map[event.EventSource]utils.ROWindow{}
		}

		// TODO: We might want to process the requests and group them in the future so
		// requests for the same clock but different event source can be done in one go
		for _, d := range clk.ProcessData() {
			if d == nil {
				continue
			}
			if d.ProcessName == req.Source {
				out[req.ClockID][req.Source] = &d.Window
			}
		}
	}
	return out
}

// RemoveAllClocks tears down all registered clocks and cleans up associated state.
func (m *ClockManager) RemoveAllClocks() {
	m.clockManagementMu.Lock()
	m.osClock.Reset()

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
	ptp, ok := ev.Data.(*event.OffsetData)
	if !ok {
		glog.Warningf("handleOSClockEvent: received unexpected event")
		return
	}
	m.osClock.AddEvent(ev)
	if m.osClock.State == ptp.State {
		return
	}
	m.osClock.State = ptp.State
	m.signalSyncStatus()

	// if the OS clock state changed, emit the event to CEP and also pass it along to each clock
	osOffset := ptp.Offset
	m.sendIPC(ipc.Message{
		Type:   ipc.TypeOSClockState,
		IFace:  ev.IFace,
		Values: ipc.StateValue{State: event.PtpStateToIPCState(m.osClock.State), Offset: osOffset},
	})
	for _, clk := range m.clocks {
		clk.SystemClockUpdate()
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
			if ev.Reset {
				m.reset(ev)
				m.clockManagementMu.Unlock()
				continue
			}

			// phc2sys/chronyd offset samples drive the OS clock. A phc2sys HA source
			// selection (SelectedSourceData) is not an OS-clock sample — it is routed
			// to its clock (the HA clock) by config name, so let it fall through.
			if ev.Source == event.CHRONYD {
				m.handleOSClockEvent(ev)
				m.clockManagementMu.Unlock()
				continue
			}
			if ev.Source == event.PHC2SYS {
				if _, isSelection := ev.Data.(*event.SelectedSourceData); !isSelection {
					m.handleOSClockEvent(ev)
					m.clockManagementMu.Unlock()
					continue
				}
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
			if clockState.LeadingIFace != "" && clockState.LeadingIFace != event.LEADING_INTERFACE_UNKNOWN {
				// BC/OC clock_state is scraped as process="ptp4l" (ptp4l is the
				// servo), whereas GM/T-BC report under their clock-type label. The
				// event's clock type is checked too: an HA clock is a composite whose
				// member events carry ClockType=BC, and those must still report as
				// ptp4l like a standalone BC follower.
				process := string(ev.ClockType)
				if clk.ClockType() == event.BC || clk.ClockType() == event.OC ||
					ev.ClockType == event.BC || ev.ClockType == event.OC {
					process = string(event.PTP4l)
				}
				m.updateClockStateMetrics(clockState.State, process, alias.GetAlias(clockState.LeadingIFace))
			}
			// Use the clock class from the event's returned state, not clk.ClockClass().
			// For a composite HA clock, clk.ClockClass() is the active member's class,
			// but the metric is keyed per member config (lookupName), so each member
			// must report its own class. For GM/TBC/BC the returned state carries the
			// same class clk.ClockClass() would, so this is equivalent for them.
			m.updateClockClassMetrics(lookupName, clockState.ClockClass)
			m.updateHAProfileMetrics(clockState.HAProfileStatus)
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
		processLabel: process, nodeLabel: m.nodeName, "iface": iFace}
	switch state {
	case event.PTP_LOCKED:
		m.clockMetric.With(labels).Set(event.ClockStateLocked)
	case event.PTP_HOLDOVER:
		m.clockMetric.With(labels).Set(event.ClockStateHoldover)
	default:
		m.clockMetric.With(labels).Set(event.ClockStateFreerun)
	}
}

// updateHAProfileMetrics sets the openshift_ptp_ha_profile_status gauge for each
// HA member profile (1 = ACTIVE, 0 = INACTIVE). status is nil for every non-HA
// clock, in which case this is a no-op. The label set matches the legacy metric:
// process="phc2sys", node, profile.
func (m *ClockManager) updateHAProfileMetrics(status map[string]bool) {
	if m.haMetric == nil || status == nil {
		return
	}
	for profile, active := range status {
		value := 0.0
		if active {
			value = 1.0
		}
		m.haMetric.With(prometheus.Labels{
			processLabel: string(event.PHC2SYS), nodeLabel: m.nodeName, "profile": profile,
		}).Set(value)
	}
}

// updateClockClassMetrics updates the clock class gauge for a config. The class
// is emitted under the ptp4l config name and process (matching downstream
// consumers regardless of the source event); an uninitialized (0) class is
// skipped since it means the clock has not yet determined its class.
func (m *ClockManager) updateClockClassMetrics(cfgName string, clockClass fbprotocol.ClockClass) {
	if m.clockClassMetric == nil {
		return
	}
	if clockClass == protocol.ClockClassUninitialized {
		return
	}
	profile := strings.Replace(cfgName, "ts2phc", "ptp4l", 1)
	m.clockClassMetric.With(prometheus.Labels{
		processLabel: "ptp4l", nodeLabel: m.nodeName, configLabel: profile}).Set(float64(clockClass))
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
	case *event.OffsetData:
		processData = map[event.ValueType]interface{}{
			event.OFFSET: data.Offset,
		}
		if data.NMEALocked != nil {
			processData[event.NMEA_STATUS] = *data.NMEALocked
		}
	case *event.DPLLData:
		processData = map[event.ValueType]interface{}{}
		if data.Offset != nil {
			processData[event.OFFSET] = *data.Offset
		}
		if data.PhaseStatus != nil {
			processData[event.PHASE_STATUS] = *data.PhaseStatus
		}
		if data.FrequencyStatus != nil {
			processData[event.FREQUENCY_STATUS] = *data.FrequencyStatus
		}
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
			processLabel: string(ev.Source), "iface": iface}

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
					}, []string{"from", nodeLabel, processLabel, "iface"})
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
