package event

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Data ...
type Data struct {
	ProcessName EventSource    // ts2phc  // dppl
	Details     []*DataDetails // array of iface and  offset
	State       PTPState       // have the worst state here
	logData     string         // iface that is connected to GNSS
}

// DataMetrics ...
type DataMetric struct {
	isRegistered  bool
	GaugeMetric   *prometheus.GaugeVec
	CounterMetric *prometheus.Counter
	Name          string
	ValueType     prometheus.ValueType
	Labels        prometheus.Labels
	Value         float64
}

// DataDetails .. details for data
type DataDetails struct {
	IFace        string
	State        PTPState
	ClockType    ClockType
	Metrics      map[ValueType]DataMetric
	time         int64
	logData      string
	signalSource EventSource // GNSS PPS
	sourceLost   bool
}

func (d *Data) UpdateState() {
	state := PTP_UNKNOWN
	for _, detail := range d.Details { // 2 ts2phc or 2 dpll etc
		switch detail.State {
		case PTP_FREERUN: // if its free ru and main state is not holdover then this is the state
			if state != PTP_HOLDOVER {
				state = detail.State
			}
		case PTP_HOLDOVER: // if one of them is in holdover then this is the state
			state = detail.State
		case PTP_LOCKED: // if this is locked and none of them are in UNKNOWN or FREE run then this is the state
			if state != PTP_FREERUN && state != PTP_HOLDOVER { // previous state
				state = detail.State
			}
		}
	}
	d.State = state
}
