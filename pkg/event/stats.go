package event

import "github.com/prometheus/client_golang/prometheus"

// Data ...
type Data struct {
	ProcessName EventSource
	IFace       string
	State       PTPState
	ClockType   ClockType
	Metrics     map[ValueType]DataMetrics
	time        int64
	logData     string
	sourceLost  bool
}

// DataMetrics ...
type DataMetrics struct {
	isRegistered  bool
	GaugeMetric   *prometheus.GaugeVec
	CounterMetric *prometheus.Counter
	Name          string
	ValueType     prometheus.ValueType
	Labels        prometheus.Labels
	Value         float64
}
