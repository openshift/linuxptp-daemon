package event

import (
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/prometheus/client_golang/prometheus"
)

type DDetails []*DataDetails

// Data ...
type Data struct {
	ProcessName EventSource // ts2phc  // dpll
	Details     DDetails    // array of iface and  offset
	State       PTPState    // have the worst state here
	LogData     string      // iface that is connected to GNSS
	Window      utils.Window
}

// DataMetrics ...
type DataMetric struct {
	IsRegistered  bool
	GaugeMetric   *prometheus.GaugeVec
	CounterMetric *prometheus.Counter
	Name          string
	ValueType     prometheus.ValueType
	Labels        prometheus.Labels
	Value         float64
}

// DataDetails .. details for data
type DataDetails struct {
	IFace              string
	State              PTPState
	ClockType          ClockType
	Metrics            map[ValueType]DataMetric
	Time               int64
	LogData            string
	SignalSource       EventSource // GNSS PPS
	SourceLost         bool
	Offset             int64
	OutOfSpec          bool
	FrequencyTraceable bool
}

// UpdateState .. update process state
func (d *Data) UpdateState() {
	state := PTP_UNKNOWN
	for _, detail := range d.Details { // 2 ts2phc or 2 dpll etc
		switch detail.State {
		case PTP_FREERUN: // FREERUN is the worst state (S0) and always takes priority
			state = detail.State
		case PTP_HOLDOVER: // HOLDOVER (S1) takes priority over LOCKED but not FREERUN
			if state != PTP_FREERUN {
				state = detail.State
			}
		case PTP_LOCKED: // LOCKED (S2) is best; only sets if nothing worse exists
			if state != PTP_FREERUN && state != PTP_HOLDOVER {
				state = detail.State
			}
		}
	}
	d.State = state
	if len(d.Details) > 1 {
		for _, detail := range d.Details {
			glog.Infof("state updated for %s: port %s state=%s offset=%d",
				d.ProcessName, detail.IFace, detail.State, detail.Offset)
		}
	} else {
		glog.Infof("state updated for %s =%s", d.ProcessName, d.State)
	}
}

// GetDataDetails ...
func (d *Data) GetDataDetails(iface string) *DataDetails {
	for _, d := range d.Details {
		if d.IFace == iface {
			return d
		}
	}
	return nil
}

// AddEvent records an incoming event into the data store.
func (d *Data) AddEvent(event Event) {
	var state PTPState
	var sourceLost bool
	var offset int64
	var hasOffset bool
	var outOfSpec, frequencyTraceable bool

	switch data := event.Data.(type) {
	case *GNSSData:
		sourceLost = data.SourceLost
		offset = data.Offset
		hasOffset = true
		if data.GPSStatus >= 3 && !data.SourceLost {
			state = PTP_LOCKED
		} else {
			state = PTP_FREERUN
		}
	case *PTPData:
		state = data.State
		sourceLost = data.SourceLost
		outOfSpec = data.OutOfSpec
		frequencyTraceable = data.FrequencyTraceable
		if off, fnd := data.Values[OFFSET]; fnd {
			offset = off.(int64)
			hasOffset = true
		}
	}

	for _, dd := range d.Details {
		if dd.IFace == event.IFace {
			if dd.Time <= event.Time {
				dd.State = state
				dd.SourceLost = sourceLost
				dd.OutOfSpec = outOfSpec
				dd.FrequencyTraceable = frequencyTraceable
				dd.ClockType = event.ClockType
				dd.Time = event.Time
				dd.LogData = event.GetLogData()
				if hasOffset {
					dd.Offset = offset
					d.Window.Insert(float64(offset))
				}
			} else {
				glog.Infof("discarding stale event for process %s, last event @ %d, current event @ %d", event.Source, dd.Time, event.Time)
			}
			return
		}
	}

	details := &DataDetails{
		ClockType:          event.ClockType,
		Metrics:            map[ValueType]DataMetric{},
		IFace:              event.IFace,
		Time:               event.Time,
		LogData:            event.GetLogData(),
		State:              state,
		SourceLost:         sourceLost,
		OutOfSpec:          outOfSpec,
		FrequencyTraceable: frequencyTraceable,
	}
	if ptp, ok := event.Data.(*PTPData); ok {
		leading, found := ptp.Values[LeadingSource]
		if found && leading.(bool) {
			glog.Info(details.IFace, " is set as the leading source ")
			details.SignalSource = PTP4l
		}
	}
	d.LogData = details.LogData
	d.Details = append(d.Details, details)
}

// toString ... data details
func (dd DDetails) toString() string {
	out := strings.Builder{}
	for _, d := range dd {
		out.WriteString("  Iface name: " + d.IFace)
		out.WriteString("  state: " + string(d.State))
		out.WriteString("  clock type: " + string(d.ClockType))
		out.WriteString(" signal source: " + string(d.SignalSource))
		out.WriteString(" source lost: " + strconv.FormatBool(d.SourceLost))
		out.WriteString("-----\r\n")
	}
	return out.String()
}
