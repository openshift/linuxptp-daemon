package clockmgr

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

// requireGaugeValue checks an exported series without creating it via With.
func requireGaugeValue(t *testing.T, gauge *prometheus.GaugeVec, labels prometheus.Labels, want float64) {
	t.Helper()
	registry := prometheus.NewRegistry()
	registry.MustRegister(gauge)
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, metric := range family.Metric {
			if len(metric.Label) != len(labels) {
				continue
			}
			matches := true
			for _, label := range metric.Label {
				value, ok := labels[label.GetName()]
				if !ok || value != label.GetValue() {
					matches = false
					break
				}
			}
			if matches {
				require.Equal(t, want, metric.GetGauge().GetValue())
				return
			}
		}
	}
	t.Fatalf("metric series not exported: %v", labels)
}

func newProcessMetricManager(t *testing.T, clockType event.ClockType) (*ClockManager, *prometheus.GaugeVec) {
	t.Helper()
	gauge := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_process_clock_state"},
		[]string{processLabel, nodeLabel, ifaceLabel})
	// Process state must be exported even without an offset collector.
	cm := Init("testnode", make(chan event.Event), nil, gauge, nil, nil)
	var clk clock.Clock
	var err error
	switch clockType {
	case event.GM:
		clk, err = clock.NewGM(testClockmgrPtp4lConfig, func() int { return 37 }, &pmc.MockClient{})
	case event.TBC:
		clk, err = clock.NewTBC(testClockmgrPtp4lConfig, func() int { return 37 }, &pmc.MockClient{}, "")
	default:
		t.Fatalf("unsupported test clock type %s", clockType)
	}
	require.NoError(t, err)
	require.NoError(t, cm.AddClock(clk))
	return cm, gauge
}

func TestProcessClockStateMetrics(t *testing.T) {
	for _, clockType := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(clockType), func(t *testing.T) {
			cm, gauge := newProcessMetricManager(t, clockType)
			const iface = "process-metric-port"
			alias.SetAlias(iface, "process-metric-alias")
			t.Cleanup(alias.ClearAliases)
			labels := prometheus.Labels{processLabel: "gnss", nodeLabel: "testnode", ifaceLabel: "process-metric-alias"}
			tests := []struct {
				source event.EventSource
				data   event.EventData
				want   float64
			}{
				{event.GNSS, &event.GNSSData{GPSStatus: 2}, event.ClockStateFreerun},
				{event.GNSS, &event.GNSSData{GPSStatus: 3}, event.ClockStateLocked},
				{event.GNSS, &event.GNSSData{GPSStatus: 3, SourceLost: true}, event.ClockStateFreerun},
				{event.GNSS, &event.GNSSData{GPSStatus: 3}, event.ClockStateLocked},
				{event.DPLL, &event.DPLLData{State: event.PTP_LOCKED, LeadingSource: true}, event.ClockStateLocked},
				{event.DPLL, &event.DPLLData{State: event.PTP_HOLDOVER}, event.ClockStateHoldover},
				{event.DPLL, &event.DPLLData{State: event.PTP_FREERUN}, event.ClockStateFreerun},
				{event.DPLL, &event.DPLLData{State: event.PTP_LOCKED}, event.ClockStateLocked},
			}
			for i, tt := range tests {
				cm.ProcessEvent(event.Event{Source: tt.source, CfgName: testClockmgrPtp4lConfig,
					ClockType: clockType, IFace: iface, Time: int64(i + 1), Data: tt.data})
				labels[processLabel] = string(tt.source)
				requireGaugeValue(t, gauge, labels, tt.want)
			}

			// An older sample must not overwrite the accepted state.
			cm.ProcessEvent(event.Event{Source: event.DPLL, CfgName: testClockmgrPtp4lConfig,
				ClockType: clockType, IFace: iface, Time: 1, Data: &event.DPLLData{State: event.PTP_FREERUN}})
			requireGaugeValue(t, gauge, labels, event.ClockStateLocked)

			// DPLL ports have independent state, not the worst-of-process state.
			cm.ProcessEvent(event.Event{Source: event.DPLL, CfgName: testClockmgrPtp4lConfig,
				ClockType: clockType, IFace: "other-port", Time: 10, Data: &event.DPLLData{State: event.PTP_FREERUN}})
			requireGaugeValue(t, gauge, labels, event.ClockStateLocked)
			otherLabels := prometheus.Labels{processLabel: "dpll", nodeLabel: "testnode", ifaceLabel: "other-port"}
			requireGaugeValue(t, gauge, otherLabels, event.ClockStateFreerun)

			// Reset removes the restored process series without deleting GNSS.
			cm.ProcessEvent(event.Event{Source: event.DPLL, CfgName: testClockmgrPtp4lConfig, Reset: true})
			require.False(t, gauge.Delete(labels))
			require.False(t, gauge.Delete(otherLabels))
			labels[processLabel] = "gnss"
			requireGaugeValue(t, gauge, labels, event.ClockStateLocked)
		})
	}
}

func TestSyncEClockStateMetrics(t *testing.T) {
	cm, gauge := newProcessMetricManager(t, event.TBC)
	const cfgName = "synce4l.0.config"
	const iface = "synce-metric-port"
	alias.SetAlias(iface, "synce-metric-alias")
	t.Cleanup(alias.ClearAliases)
	labels := prometheus.Labels{processLabel: string(event.SYNCE), nodeLabel: "testnode", ifaceLabel: "synce-metric-alias"}
	send := func(data *event.SyncEData) {
		cm.ProcessEvent(event.Event{Source: event.SYNCE, CfgName: cfgName, IFace: iface, Data: data})
	}
	// Quality can be reported before the first EEC state, without implying lock.
	send(&event.SyncEData{State: event.PTP_UNKNOWN})
	require.False(t, gauge.Delete(labels))
	before := cm.GetClock(testClockmgrPtp4lConfig).GetState()
	for _, tt := range []struct {
		state event.PTPState
		want  float64
	}{
		{event.PTP_LOCKED, event.ClockStateLocked},
		{event.PTP_HOLDOVER, event.ClockStateHoldover},
		{event.PTP_FREERUN, event.ClockStateFreerun},
		{event.PTP_LOCKED, event.ClockStateLocked},
	} {
		send(&event.SyncEData{State: tt.state, EECState: "test EEC state"})
		requireGaugeValue(t, gauge, labels, tt.want)
	}
	send(&event.SyncEData{State: event.PTP_UNKNOWN})
	requireGaugeValue(t, gauge, labels, event.ClockStateLocked)
	require.Equal(t, before, cm.GetClock(testClockmgrPtp4lConfig).GetState(), "SyncE must not change aggregate PTP state")
	cm.ProcessEvent(event.Event{Source: event.SYNCE, CfgName: cfgName, Reset: true})
	require.False(t, gauge.Delete(labels), "reset must remove SyncE state series")
}

func TestProcessClockStateMetricsWithoutInterface(t *testing.T) {
	for _, iface := range []string{"", event.LEADING_INTERFACE_UNKNOWN} {
		t.Run(iface, func(t *testing.T) {
			cm, gauge := newProcessMetricManager(t, event.GM)
			cm.ProcessEvent(event.Event{Source: event.GNSS, CfgName: testClockmgrPtp4lConfig,
				ClockType: event.GM, IFace: iface, Data: &event.GNSSData{GPSStatus: 3}})
			require.False(t, gauge.Delete(prometheus.Labels{
				processLabel: "gnss", nodeLabel: "testnode", ifaceLabel: iface,
			}))
		})
	}
}
