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
	HasOffset          bool
	ProcessStatus      int64
	HasProcessStatus   bool
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
	var leading bool

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
	case *OffsetData:
		state = data.State
		sourceLost = data.SourceLost
		offset = data.Offset
		hasOffset = true
	case *DPLLData:
		state = data.State
		sourceLost = data.SourceLost
		outOfSpec = data.OutOfSpec
		frequencyTraceable = data.FrequencyTraceable
		if data.Offset != nil {
			offset = *data.Offset
			hasOffset = true
		}
		if data.LeadingSource {
			leading = true
		}
	case *StateData:
		state = data.State
		sourceLost = data.SourceLost
	case *SyncEData:
		state = data.State
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
					dd.HasOffset = true
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
	if leading {
		glog.Info(details.IFace, " is set as the leading source ")
		details.SignalSource = PTP4l
	}
	d.LogData = details.LogData
	d.Details = append(d.Details, details)
}

// String prints ProcessName and each populated DataDetails.
func (d *Data) String() string {
	out := strings.Builder{}
	out.WriteString(string(d.ProcessName))
	for _, dd := range d.Details {
		if dd == nil {
			continue
		}
		out.WriteString(" {")
		out.WriteString(dd.String())
		out.WriteByte('}')
	}
	return out.String()
}

// String emits populated fields only.
func (dd *DataDetails) String() string {
	parts := make([]string, 0, 5)
	if dd.IFace != "" {
		parts = append(parts, "iface="+dd.IFace)
	}
	switch dd.State {
	case PTP_FREERUN, PTP_HOLDOVER, PTP_LOCKED:
		parts = append(parts, "state="+string(dd.State))
	}
	if dd.HasOffset {
		parts = append(parts, "offset="+strconv.FormatInt(dd.Offset, 10))
	}
	if dd.SourceLost {
		parts = append(parts, "sourceLost=true")
	}
	if dd.HasProcessStatus {
		parts = append(parts, "process_status="+strconv.FormatInt(dd.ProcessStatus, 10))
	}
	return strings.Join(parts, " ")
}

// Summary prints ProcessName, detail count, and the latest populated value of each field.
func (d *Data) Summary() string {
	if d == nil {
		return ""
	}
	var (
		n              int
		iface          string
		ifaceTime      int64
		haveIFace      bool
		state          PTPState
		stateTime      int64
		haveState      bool
		offset         int64
		offsetTime     int64
		haveOffset     bool
		sourceLost     bool
		sourceLostTime int64
		haveSourceLost bool
		processStatus  int64
		statusTime     int64
		haveStatus     bool
	)
	for _, dd := range d.Details {
		if dd == nil {
			continue
		}
		n++
		if dd.IFace != "" && (!haveIFace || dd.Time >= ifaceTime) {
			iface = dd.IFace
			ifaceTime = dd.Time
			haveIFace = true
		}
		if ptpStatePopulated(dd.State) && (!haveState || dd.Time >= stateTime) {
			state = dd.State
			stateTime = dd.Time
			haveState = true
		}
		if dd.HasOffset && (!haveOffset || dd.Time >= offsetTime) {
			offset = dd.Offset
			offsetTime = dd.Time
			haveOffset = true
		}
		if (ptpStatePopulated(dd.State) || dd.HasOffset) && (!haveSourceLost || dd.Time >= sourceLostTime) {
			sourceLost = dd.SourceLost
			sourceLostTime = dd.Time
			haveSourceLost = true
		}
		if dd.HasProcessStatus && (!haveStatus || dd.Time >= statusTime) {
			processStatus = dd.ProcessStatus
			statusTime = dd.Time
			haveStatus = true
		}
	}
	parts := []string{string(d.ProcessName), "n=" + strconv.Itoa(n)}
	if haveIFace {
		parts = append(parts, "iface="+iface)
	}
	if haveState {
		parts = append(parts, "state="+string(state))
	}
	if haveOffset {
		parts = append(parts, "offset="+strconv.FormatInt(offset, 10))
	}
	if haveSourceLost && sourceLost {
		parts = append(parts, "sourceLost=true")
	}
	if haveStatus {
		parts = append(parts, "process_status="+strconv.FormatInt(processStatus, 10))
	}
	return strings.Join(parts, " ")
}

func ptpStatePopulated(s PTPState) bool {
	switch s {
	case PTP_FREERUN, PTP_HOLDOVER, PTP_LOCKED:
		return true
	}
	return false
}
