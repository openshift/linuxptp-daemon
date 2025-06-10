package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"sync"
)

var registerMetrics sync.Once

const (
	PTPNamespace = "openshift"
	PTPSubsystem = "ptp"
)

var (
	Offset = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "offset_ns",
			Help:      "",
		}, []string{"from", "process", "node", "iface"})

	MaxOffset = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "max_offset_ns",
			Help:      "",
		}, []string{"from", "process", "node", "iface"})

	FrequencyAdjustment = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "frequency_adjustment_ns",
			Help:      "",
		}, []string{"from", "process", "node", "iface"})

	Delay = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "delay_ns",
			Help:      "",
		}, []string{"from", "process", "node", "iface"})

	// ClockState metrics to show current clock state
	ClockState = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "clock_state",
			Help:      "0 = FREERUN, 1 = LOCKED, 2 = HOLDOVER",
		}, []string{"process", "node", "iface"})

	// ClockClassMetrics metrics to show current clock class
	ClockClassMetrics = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "clock_class",
			Help:      "6 = Locked, 7 = PRC unlocked in-spec, 52/187 = PRC unlocked out-of-spec, 135 = T-BC holdover in-spec, 165 = T-BC holdover out-of-spec, 248 = Default, 255 = Slave Only Clock",
		}, []string{"process", "node"})

	// InterfaceRole metrics to show current interface role
	InterfaceRole = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "interface_role",
			Help:      "0 = PASSIVE, 1 = SLAVE, 2 = MASTER, 3 = FAULTY, 4 = UNKNOWN, 5 = LISTENING",
		}, []string{"process", "node", "iface"})

	ProcessStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "process_status",
			Help:      "0 = DOWN, 1 = UP",
		}, []string{"process", "node", "config"})

	ProcessRestartCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "process_restart_count",
			Help:      "",
		}, []string{"process", "node", "config"})

	// PTPHAMetrics metrics to show current ha profiles
	PTPHAMetrics = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "ha_profile_status",
			Help:      "0 = INACTIVE 1 = ACTIVE",
		}, []string{"process", "node", "profile"})

	// SynceClockQL  metrics to show current synce Clock Qulity
	SynceClockQL = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "synce_clock_quality",
			Help:      "Help: network_option1: ePRTC = 32 PRTC = 34 PRC =257  SSU-A = 259 SSU-B = 263 EEC1 = 266 QL-DNU =270 network_option2: ePRTC = 34 PRTC = 33 PRS =256 STU = 255 ST2 = 262 TNC = 259 ST3E =268 EEC2 =265 PROV =269 QL-DUS =270",
		}, []string{"process", "node", "profile", "network_option", "iface", "device"})

	// SynceQLInfo metrics to show current QL values
	SynceQLInfo = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: PTPNamespace,
			Subsystem: PTPSubsystem,
			Name:      "synce_ssm_ql",
			Help: "network_option1: ePRTC: {0, 0x2, 0x21}, PRTC:  {1, 0x2, 0x20}, PRC:   {2, 0x2, 0xFF}, SSUA:  {3, 0x4, 0xFF}, SSUB:  {4, 0x8, 0xFF}, EEC1:  {5, 0xB, 0xFF},QL-DNU: {6,0xF,0xFF}\n " +
				"   network_option2 ePRTC: {0, 0x1, 0x21}, PRTC:  {1, 0x1, 0x20}, PRS:   {2, 0x1, 0xFF}, STU:   {3, 0x0, 0xFF}, ST2:   {4, 0x7, 0xFF}, TNC:   {5, 0x4, 0xFF}, ST3E:  {6, 0xD, 0xFF}, EEC2:  {7, 0xA, 0xFF}, PROV:  {8, 0xE, 0xFF}, QL-DUS: {9,0xF,0xFF}",
		}, []string{"process", "node", "profile", "network_option", "iface", "device", "ql_type"})
)

// RegisterMetrics registers all the metrics with Prometheus
func RegisterMetrics(nodeName string) {
	registerMetrics.Do(func() {
		prometheus.MustRegister(Offset)
		prometheus.MustRegister(MaxOffset)
		prometheus.MustRegister(FrequencyAdjustment)
		prometheus.MustRegister(Delay)
		prometheus.MustRegister(InterfaceRole)
		prometheus.MustRegister(ClockState)
		prometheus.MustRegister(ProcessStatus)
		prometheus.MustRegister(ProcessRestartCount)
		prometheus.MustRegister(ClockClassMetrics)
		prometheus.MustRegister(PTPHAMetrics)
		prometheus.MustRegister(SynceQLInfo)
		prometheus.MustRegister(SynceClockQL)

		// Including these stats kills performance when Prometheus polls with multiple targets
		prometheus.Unregister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
		prometheus.Unregister(collectors.NewGoCollector())

		NodeName = nodeName
	})

}
