package clock

import (
	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

// OSClockFilterConfig configures the phc2sys disciplining-state filter.
type OSClockFilterConfig struct {
	InSyncThreshold    float64
	OutOfSyncThreshold float64
	Samples            int
}

// OsClock owns OS-clock measurements and their evaluated synchronization state.
// State is the disciplining state; E3State is the last reported state derived
// from State and the registered PTP clocks. They must not be conflated.
type OsClock struct {
	DataHolder
	State            event.PTPState
	E3State          event.PTPState
	IFace            string
	Offset           int64
	filter           utils.HysteresisFilter
	filteredState    event.PTPState
	filterConfigured bool
}

// ConfigureFilter starts a fresh phc2sys filter with the resolved profile settings.
func (o *OsClock) ConfigureFilter(cfg OSClockFilterConfig) {
	o.filter.Config(cfg.InSyncThreshold, cfg.OutOfSyncThreshold, cfg.Samples, cfg.Samples)
	o.filter.Reset()
	o.filteredState = event.PTP_NOTSET
	o.filterConfigured = true
}

// Reset clears samples and evaluated states, retaining the filter configuration.
func (o *OsClock) Reset() {
	o.State = event.PTP_FREERUN
	o.E3State = event.PTP_NOTSET
	o.IFace = "CLOCK_REALTIME"
	o.Offset = 0
	o.filteredState = event.PTP_NOTSET
	o.filter.Reset()
	o.DataHolder.Reset()
}

// GetData returns source data. PHC2SYS needs only the latest sample and filtered
// state, not a second offset history for startup qualification.
func (o *OsClock) GetData(source event.EventSource) *event.Data {
	return o.getData(source, source != event.PHC2SYS)
}

// AddEvent evaluates each OS-clock sample and records its latest details.
// PHC2SYS readiness comes from the hysteresis filter, without an offset window.
// CHRONYD remains unfiltered.
// The original event is never modified: other consumers may need the raw state.
// It returns true when the event was accepted, even during debounce or when the
// state is unchanged. Rejected events remain available for normal clock routing.
func (o *OsClock) AddEvent(ev event.Event) bool {
	ptp, ok := ev.Data.(*event.OffsetData)
	if !ok || ptp == nil || (ev.Source != event.CHRONYD && ev.Source != event.PHC2SYS) {
		return false
	}
	o.IFace = ev.IFace
	o.Offset = ptp.Offset
	state := ptp.State
	if ev.Source == event.PHC2SYS && o.filterConfigured {
		state = o.updateState(float64(ptp.Offset), ptp.State)
	}
	// Store the evaluated source state alongside the unmodified latest offset.
	data := *ptp
	data.State = state
	ev.Data = &data
	d := o.GetData(ev.Source)
	first := len(d.Details) == 0
	d.AddEvent(ev)
	if ev.Source == event.CHRONYD && first {
		// Data.AddEvent creates the first detail without inserting its offset.
		d.Window.Insert(float64(ptp.Offset))
	}
	d.UpdateState()
	if state == event.PTP_NOTSET {
		// Keep the externally valid initial/previous state during debounce, but
		// expose NOTSET to conditions waiting for a settled phc2sys state.
		d.State = event.PTP_NOTSET
		return true
	}
	o.State = state
	return true
}

func (o *OsClock) updateState(offset float64, rawState event.PTPState) event.PTPState {
	if rawState == event.PTP_HOLDOVER {
		if o.filteredState != event.PTP_HOLDOVER {
			o.filter.Reset()
		}
		o.filteredState = event.PTP_HOLDOVER
		return o.filteredState
	}
	state, _ := o.filter.Update(offset)
	switch state {
	case utils.HysteresisLow:
		o.filteredState = event.PTP_LOCKED
	case utils.HysteresisHigh:
		o.filteredState = event.PTP_FREERUN
	default:
		o.filteredState = event.PTP_NOTSET
	}
	glog.V(14).Infof("OS clock sample: raw state %s, offset %v, filtered state %s", rawState, offset, o.filteredState)
	return o.filteredState
}

// DeriveOSClockState computes the O-RAN E3 OS-clock sync state from the PTP
// source traceability (E1) and the phc2sys disciplining status:
//
//	E3 = worst-of(E1, phc2sys)
//
// The OS clock can never report LOCKED while its PTP reference is in HOLDOVER,
// FREERUN or NOTSET. A NOTSET input yields NOTSET, which is internal and must
// not be emitted. OsClock starts in the externally valid FREERUN state.
func DeriveOSClockState(e1, phc2sys event.PTPState) event.PTPState {
	return event.WorstOfState(e1, phc2sys)
}
