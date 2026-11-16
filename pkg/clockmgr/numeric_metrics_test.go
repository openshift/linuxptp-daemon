package clockmgr

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func newNumericMetricManager(t *testing.T) *ClockManager {
	t.Helper()
	// Exercise dynamic registration without leaking collectors between tests.
	previous := prometheus.DefaultRegisterer
	prometheus.DefaultRegisterer = prometheus.NewRegistry()
	t.Cleanup(func() { prometheus.DefaultRegisterer = previous })
	offset := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_process_offset_ns"},
		[]string{fromLabel, processLabel, nodeLabel, ifaceLabel})
	return Init("testnode", make(chan event.Event), offset, nil, nil, nil)
}

func TestDPLLStatusMetrics(t *testing.T) {
	cm := newNumericMetricManager(t)
	const iface = "dpll-status-port"
	alias.SetAlias(iface, "dpll-status-alias")
	t.Cleanup(alias.ClearAliases)
	labels := prometheus.Labels{fromLabel: "dpll", processLabel: "dpll", nodeLabel: "testnode", ifaceLabel: "dpll-status-alias"}
	offset, phase, frequency := int64(-12), int64(2), int64(3)
	ev := event.Event{Source: event.DPLL, CfgName: testClockmgrPtp4lConfig, IFace: iface,
		Data: &event.DPLLData{State: event.PTP_LOCKED, PPSStatus: 1, Offset: &offset, PhaseStatus: &phase, FrequencyStatus: &frequency}}
	cm.updateMetrics(ev)
	requireGaugeValue(t, cm.offsetMetric, labels, -12)
	for name, want := range map[string]float64{"pps_status": 1, "phase_status": 2, "frequency_status": 3} {
		gauge := cm.registeredGauges[name]
		require.NotNil(t, gauge, "missing %s collector", name)
		requireGaugeValue(t, gauge, labels, want)
	}
	ppsGauge := cm.registeredGauges["pps_status"]
	// A DPLL with no optional measurements must still report lost PPS. Reuse
	// the collector and update the existing series rather than leaving it stale.
	ev.Data = &event.DPLLData{State: event.PTP_HOLDOVER, PPSStatus: 0}
	cm.updateMetrics(ev)
	require.Same(t, ppsGauge, cm.registeredGauges["pps_status"])
	requireGaugeValue(t, ppsGauge, labels, 0)

	// Optional hardware status fields must not invent zero-valued metrics.
	ev.IFace = "dpll-without-status"
	cm.updateMetrics(ev)
	labels[ifaceLabel] = ev.IFace
	requireGaugeValue(t, ppsGauge, labels, 0)
	require.False(t, cm.offsetMetric.Delete(labels))
	require.False(t, cm.registeredGauges["phase_status"].Delete(labels))
	require.False(t, cm.registeredGauges["frequency_status"].Delete(labels))

	cm.unregisterMetrics(testClockmgrPtp4lConfig, "dpll")
	require.False(t, ppsGauge.Delete(labels))
	labels[ifaceLabel] = "dpll-status-alias"
	require.False(t, ppsGauge.Delete(labels))
	require.False(t, cm.offsetMetric.Delete(labels))
	require.False(t, cm.registeredGauges["phase_status"].Delete(labels))
	require.False(t, cm.registeredGauges["frequency_status"].Delete(labels))
}

func TestGNSSAndNMEAMetrics(t *testing.T) {
	cm := newNumericMetricManager(t)
	labels := prometheus.Labels{fromLabel: "gnss", processLabel: "gnss", nodeLabel: "testnode", ifaceLabel: "gnss-port"}
	cm.updateMetrics(event.Event{Source: event.GNSS, CfgName: testClockmgrPtp4lConfig, IFace: "gnss-port",
		Data: &event.GNSSData{GPSStatus: 3, Offset: 42}})
	requireGaugeValue(t, cm.offsetMetric, labels, 42)
	requireGaugeValue(t, cm.registeredGauges["gnss_status"], labels, 3)
	labels[fromLabel], labels[processLabel] = "master", "ts2phc"
	locked := true
	ev := event.Event{Source: event.TS2PHC, CfgName: testClockmgrPtp4lConfig, IFace: "gnss-port",
		Data: &event.OffsetData{State: event.PTP_LOCKED, Offset: 10, NMEALocked: &locked}}
	cm.updateMetrics(ev)
	requireGaugeValue(t, cm.offsetMetric, labels, 10)
	labels[fromLabel] = "ts2phc"
	requireGaugeValue(t, cm.registeredGauges["nmea_status"], labels, 1)
	locked = false
	cm.updateMetrics(ev)
	requireGaugeValue(t, cm.registeredGauges["nmea_status"], labels, 0)
}
