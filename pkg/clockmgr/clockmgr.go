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
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
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
	// statsUpdates holds the latest complete snapshot for ProcessManager. All
	// publishers are serialized by clockManagementMu; old pending snapshots
	// are replaced so a slow consumer cannot block clock processing.
	statsUpdates   chan process.EventStats
	osClockConfigs map[string]struct{}
}

// ConfigureSourceQualification sets policy for the active upstream PTP input of
// cfg's ptp4l instance, not aggregate lock or the remote GM's time reference.
// Configure before process startup; runtime observations use the event channel.
func (m *ClockManager) ConfigureSourceQualification(cfg string, ports []string, policy utils.QualificationConfig) (uint64, error) {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	clk, ok := m.clocks[cfg].(*clock.TBC)
	if !ok {
		return 0, fmt.Errorf("source qualification requires a T-BC clock for %s", cfg)
	}
	generation, err := clk.ConfigureSourceQualification(ports, policy)
	m.publishStats()
	return generation, err
}

// SourceQualification snapshots the upstream identity/epoch for a new process
// run. Startup conditions continue to consume the retained stats channel.
func (m *ClockManager) SourceQualification(cfg string) (event.SourceQualification, bool) {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	if clk, ok := m.clocks[cfg].(*clock.TBC); ok {
		return clk.SourceQualification(cfg)
	}
	return event.SourceQualification{}, false
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

// clockConfigDataProvider lets composite clocks return member-specific data
// without adding config-specific behavior to every Clock implementation.
type clockConfigDataProvider interface {
	ProcessDataForConfig(cfgName string) []*event.Data
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

// StatsUpdatesCh returns the snapshot channel for the single ProcessManager
// consumer. It is not a subscription: the channel retains the latest snapshot
// even before the manager starts, and is never closed by ClockManager.
func (m *ClockManager) StatsUpdatesCh() <-chan process.EventStats {
	return m.statsUpdates
}

// publishStats pushes a detached snapshot after every evaluated sample/reset,
// including unchanged states. Call with clockManagementMu held (or during Init).
func (m *ClockManager) publishStats() {
	requests := make([]process.StatsRequest, 0, len(m.clocks)+len(m.osClockConfigs))
	for cfg := range m.clocks {
		requests = append(requests, process.StatsRequest{ClockID: cfg})
	}
	for cfg := range m.osClockConfigs {
		if _, exists := m.clocks[cfg]; !exists {
			requests = append(requests, process.StatsRequest{ClockID: cfg})
		}
	}
	stats := m.snapshotStats(requests)
	select {
	case m.statsUpdates <- stats:
	default:
		select {
		case <-m.statsUpdates:
		default:
		}
		// Only this lock-serialized publisher writes to the capacity-one
		// channel. After removing the old value, this send cannot block.
		m.statsUpdates <- stats
	}
}

// Init ... initialize event manager
func Init(nodeName string, processChannel chan event.Event, offsetMetric *prometheus.GaugeVec, clockMetric *prometheus.GaugeVec, clockClassMetric *prometheus.GaugeVec, ipcCache *ipc.Cache) *ClockManager {
	m := &ClockManager{
		nodeName:         nodeName,
		events:           processChannel,
		metricCache:      map[metricKey]metricEntry{},
		registeredGauges: map[string]*prometheus.GaugeVec{},
		clockMetric:      clockMetric,
		offsetMetric:     offsetMetric,
		clockClassMetric: clockClassMetric,
		clocks:           map[string]clock.Clock{},
		osClock:          clock.OsClock{State: event.PTP_FREERUN, E3State: event.PTP_NOTSET, IFace: parserconstants.ClockRealTime},
		ipcCache:         ipcCache,
		syncStatusCh:     make(chan struct{}, 1),
		statsUpdates:     make(chan process.EventStats, 1),
		osClockConfigs:   make(map[string]struct{}),
	}
	m.publishStats()
	return m
}

// ConfigureOSClockFilter applies the resolved phc2sys profile settings.
func (m *ClockManager) ConfigureOSClockFilter(cfg clock.OSClockFilterConfig) {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	m.osClock.ConfigureFilter(cfg)
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
	m.publishStats()
	return nil
}

// snapshotStats builds a detached snapshot while clockManagementMu is held.
func (m *ClockManager) snapshotStats(requests []process.StatsRequest) process.EventStats {
	stats := process.EventStats{
		Windows:         make(map[string]map[event.EventSource]utils.ROWindow),
		States:          make(map[string]map[event.EventSource]event.PTPState),
		ClockStates:     make(map[string]event.PTPState),
		OSClockState:    m.osClock.State,
		OSClockE3State:  m.osClock.E3State,
		SourceQualified: make(map[string]bool),
	}
	for _, req := range requests {
		var data []*event.Data
		if clk := m.GetClock(req.ClockID); clk != nil {
			if tbc, ok := clk.(*clock.TBC); ok {
				if result, exists := tbc.SourceQualification(req.ClockID); exists && (req.Source == "" || req.Source == result.SourceType) {
					stats.SourceQualified[req.ClockID] = result.Qualified && !m.IsApplying()
				}
			}
			// HA member config names resolve to the HA composite in m.clocks.
			// Its inherited ProcessData contains only composite/OS data, not
			// the members' ptp4l states and windows. Preserve those per-member
			// samples so phc2sys startup can qualify any healthy HA member;
			// otherwise its offset-window conditions never become ready.
			if provider, ok := clk.(clockConfigDataProvider); ok {
				data = provider.ProcessDataForConfig(req.ClockID)
			} else {
				data = clk.ProcessData()
			}
			stats.ClockStates[req.ClockID] = clk.GetState()
		} else {
			data = m.osClock.ProcessData()
		}
		for _, d := range data {
			if d == nil || (req.Source != "" && d.ProcessName != req.Source) {
				continue
			}
			if stats.States[req.ClockID] == nil {
				stats.States[req.ClockID] = make(map[event.EventSource]event.PTPState)
				stats.Windows[req.ClockID] = make(map[event.EventSource]utils.ROWindow)
			}
			stats.States[req.ClockID][d.ProcessName] = d.State
			if d.Window != nil {
				stats.Windows[req.ClockID][d.ProcessName] = d.Window.Snapshot()
			}
		}
	}
	return stats
}

// RemoveAllClocks tears down all registered clocks and cleans up associated state.
func (m *ClockManager) RemoveAllClocks() {
	m.clockManagementMu.Lock()
	m.osClock.Reset()

	for cfgName := range m.clocks {
		m.unregisterMetrics(cfgName, "")
	}
	m.clocks = map[string]clock.Clock{}
	m.osClockConfigs = make(map[string]struct{})
	m.publishStats()
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

// handleOSClockEvent lets OsClock accept its own events, derives E3, and notifies
// the registered clocks when the disciplining state changes. It returns whether
// the event was handled, independently of whether the state changed.
func (m *ClockManager) handleOSClockEvent(ev event.Event) bool {
	previous := m.osClock.State
	if !m.osClock.AddEvent(ev) {
		return false
	}
	m.osClockConfigs[ev.CfgName] = struct{}{}
	derived := m.emitOSClockState()
	if m.osClock.State == previous {
		return true
	}
	m.signalSyncStatus()
	glog.V(4).Infof("OS clock: disciplining %s -> E3 %s (e1 %s)", m.osClock.State, derived, m.worstClockState())
	for _, clk := range m.clocks {
		clk.SystemClockUpdate()
	}
	return true
}

// worstClockState returns the most degraded state across registered clocks
// (FREERUN > HOLDOVER > LOCKED), or NOTSET if all states are unset/unknown.
func (m *ClockManager) worstClockState() event.PTPState {
	worst := event.PTP_NOTSET
	for _, clk := range m.clocks {
		switch clk.GetState() {
		case event.PTP_FREERUN:
			return event.PTP_FREERUN // Nothing can be worse.
		case event.PTP_HOLDOVER:
			worst = event.PTP_HOLDOVER
		case event.PTP_LOCKED:
			if worst == event.PTP_NOTSET {
				worst = event.PTP_LOCKED
			}
		}
	}
	return worst
}

// emitOSClockState derives the node-wide E3 OS-clock state as worst-of(E1,
// phc2sys) and emits an os_clock_state IPC when it changes. Returns the derived
// state. Assumes clockManagementMu is held.
func (m *ClockManager) emitOSClockState() event.PTPState {
	e1 := m.worstClockState()
	derived := clock.DeriveOSClockState(e1, m.osClock.State)
	if len(m.clocks) == 0 {
		derived = m.osClock.State
	}
	if derived == event.PTP_NOTSET || derived == m.osClock.E3State {
		return derived
	}
	m.osClock.E3State = derived
	glog.V(4).Infof("OS clock E3: %s (e1 %s, phc2sys %s)", derived, e1, m.osClock.State)
	m.sendIPC(ipc.Message{
		Type:   ipc.TypeOSClockState,
		IFace:  m.osClock.IFace,
		Values: ipc.StateValue{State: event.PtpStateToIPCState(derived), Offset: m.osClock.Offset},
	})
	return derived
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
			m.ProcessEvent(ev)
		case <-ctx.Done():
			return
		}
	}
}

// ProcessEvent evaluates one event. Production callers forward events to the
// ProcessEvents channel; the resulting snapshot is pushed to ProcessManager.
// All clock mutation is serialized by clockManagementMu.
func (m *ClockManager) ProcessEvent(ev event.Event) {
	m.clockManagementMu.Lock()
	defer m.clockManagementMu.Unlock()
	if observation, ok := ev.Data.(*event.PTPSourceObservation); ok {
		result := event.SourceQualification{}
		if clk, exists := m.clocks[ev.CfgName].(*clock.TBC); exists && !m.IsApplying() {
			result = clk.ObserveSource(ev, observation)
		}
		m.publishStats()
		// Rejected observations also get a reply; a removed/replaced clock must
		// not strand its old scanner. Never wait for hardware or a receiver.
		select {
		case observation.Reply <- result:
		default:
		}
		return
	}
	if ev.Reset {
		m.reset(ev)
		m.publishStats()
		return
	}

	// OsClock owns event acceptance. Other events continue to their clock by
	// config name, including HA source-selection events.
	if m.handleOSClockEvent(ev) {
		m.publishStats()
		return
	}

	// TODO: Move to better identifiers? Having to do this translation here is odd
	lookupName := ev.CfgName
	if ev.Source == event.SYNCE {
		lookupName = strings.Replace(ev.CfgName, "synce4l", "ptp4l", 1)
	}

	clk := m.GetClock(lookupName)
	if clk == nil {
		glog.Warningf("ProcessEvents: no clock registered for %s, skipping event", lookupName)
		return
	}

	if clk.ClockType() == event.TBC && m.IsApplying() {
		return
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
	// A clock (E1) transition may move the derived E3 os_clock_state.
	m.emitOSClockState()
	m.publishStats()
}

// reset handles a reset event
func (m *ClockManager) reset(ev event.Event) {
	debug.ClearState()
	if m.ipcCache != nil {
		m.ipcCache.Clear()
	}
	if clk := m.GetClock(ev.CfgName); clk != nil {
		if tbc, ok := clk.(*clock.TBC); ok {
			tbc.ResetSource(ev.Source)
		} else {
			clk.Reset()
		}
	}
	m.osClock.E3State = event.PTP_NOTSET
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

// convertToFloat converts an event field to float64.
// Bool values map to 1 (true) and 0 (false).
func convertToFloat(value interface{}) (float64, bool) {
	switch val := value.(type) {
	case int64:
		return float64(val), true
	case float64:
		return val, true
	case bool:
		if val {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
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
		dataValue, ok := convertToFloat(value)
		if !ok {
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
			} else if existing, exists := m.registeredGauges[metricName]; exists {
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
