package event

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/openshift/linuxptp-daemon/pkg/pmc"
	"github.com/openshift/linuxptp-daemon/pkg/protocol"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus"
)

type ValueType string

const (
	PTPNamespace = "openshift"
	PTPSubsystem = "ptp"
)
const (
	OFFSET     ValueType = "offset"
	STATE      ValueType = "state"
	GPS_STATUS ValueType = "gnss_status"
	//Status           ValueType = "status"
	PHASE_STATUS     ValueType = "phase_status"
	FREQUENCY_STATUS ValueType = "frequency_status"
)

// ClockType ...
type ClockType string

const (
	// GM ..
	GM ClockType = "GM"
	// BC ...
	BC ClockType = "BC"
	// OC ...
	OC ClockType = "OC"
)

// PTP4lProcessName ...
const PTP4lProcessName = "ptp4l"

// TS2PHCProcessName ...
const TS2PHCProcessName = "ts2phc"

// EventSource ...
type EventSource string

const (
	GNSS    EventSource = "gnss"
	DPLL    EventSource = "dpll"
	TS2PHC  EventSource = "ts2phc"
	PTP4l   EventSource = "ptp4l"
	PHC2SYS EventSource = "phc2sys"
	SYNCE   EventSource = "syncE"
)

// PTPState ...
type PTPState string

const (

	// PTP_FREERUN ...
	PTP_FREERUN PTPState = "s0"
	// PTP_HOLDOVER ...
	PTP_HOLDOVER PTPState = "s1"
	// PTP_LOCKED ...
	PTP_LOCKED PTPState = "s2"
	// PTP_UNKNOWN
	PTP_UNKNOWN PTPState = "-1"
)

// CLOCK_CLASS_CHANGE ...
const CLOCK_CLASS_CHANGE = "CLOCK_CLASS_CHANGE"

const connectionRetryInterval = 1 * time.Second

// EventHandler ... event handler to process events
type EventHandler struct {
	sync.Mutex
	nodeName             string
	stdoutSocket         string
	stdoutToSocket       bool
	processChannel       <-chan EventChannel
	closeCh              chan bool
	data                 map[string][]Data
	statusRequestChannel chan StatusRequest
	offsetMetric         *prometheus.GaugeVec
	clockMetric          *prometheus.GaugeVec
}
type StatusRequest struct {
	Source          EventSource
	CfgName         string
	ResponseChannel chan<- PTPState
}

var (
	statusRequestChannel chan StatusRequest
)

// EventChannel .. event channel to subscriber to events
type EventChannel struct {
	ProcessName EventSource         // ptp4l, gnss etc
	State       PTPState            // PTP locked etc
	IFace       string              // Interface that is causing the event
	CfgName     string              // ptp config profile name
	Values      map[ValueType]int64 // either offset or status , 3 information  offset , phase state and frequency state
	logString   string              // if logstring sent here then log is not printed by the process and it is managed here
	ClockType   ClockType           // oc bc gm
	Time        int64               // time.Unix.Now()
	WriteToLog  bool                // send to log in predefined format %s[%d]:[%s] %s %d
	Reset       bool                // reset data on ptp deletes or process died
}

var (
	mockTest bool = false
)

// MockEnable ...
func (e *EventHandler) MockEnable() {
	mockTest = true
}

// Init ... initialize event manager
func Init(nodeName string, stdOutToSocket bool, socketName string, processChannel chan EventChannel, closeCh chan bool, offsetMetric *prometheus.GaugeVec, clockMetric *prometheus.GaugeVec) *EventHandler {
	statusRequestChannel = make(chan StatusRequest)
	ptpEvent := &EventHandler{
		nodeName:             nodeName,
		stdoutSocket:         socketName,
		stdoutToSocket:       stdOutToSocket,
		closeCh:              closeCh,
		processChannel:       processChannel,
		data:                 map[string][]Data{},
		statusRequestChannel: statusRequestChannel,
		clockMetric:          clockMetric,
		offsetMetric:         offsetMetric,
	}
	return ptpEvent

}
func (e *EventHandler) getGMState(cfgName string) PTPState {
	lowestState := ""
	if data, ok := e.data[cfgName]; ok {
		for i, d := range data {
			if i == 0 || string(d.State) < lowestState {
				lowestState = string(d.State)
			}
		}
	}
	//gated
	if lowestState == "" {
		lowestState = "-1"
	}
	return PTPState(lowestState)
}

// ProcessEvents ... process events to generate new events
func (e *EventHandler) ProcessEvents() {
	var c net.Conn
	var err error
	defer func() {
		if e.stdoutToSocket && c != nil {
			if err = c.Close(); err != nil {
				glog.Errorf("closing connection returned error %s", err)
			}
		}
	}()
	go func() {
	connect:
		select {
		case <-e.closeCh:
			return
		default:
			if e.stdoutToSocket {
				c, err = net.Dial("unix", e.stdoutSocket)
				if err != nil {
					glog.Errorf("event process error trying to connect to event socket %s", err)
					time.Sleep(connectionRetryInterval)
					goto connect
				}
			}
		}
		glog.Info("Starting event monitoring...")
		// listen To any requests
		go e.listenToStateRequest()
		lastGmState := PTP_UNKNOWN
		gmStateInitalized := false
		for {
			select {
			case event := <-e.processChannel:
				// ts2phc[123455]:[ts2phc.0.config] 12345 s0 offset/gps
				// replace ts2phc logs here
				var logOut []string
				if event.WriteToLog {
					var logData []string
					for k, v := range event.Values {
						logData = append(logData, fmt.Sprintf("%s %d", k, v))
					}
					logDataValues := strings.Join(logData, " ")
					logOut = append(logOut, fmt.Sprintf("%s[%d]:[%s] %s %s\n", event.ProcessName,
						time.Now().Unix(), event.CfgName, logDataValues, event.State))
				}
				if event.Reset { // clean up
					if event.ProcessName == TS2PHC {
						e.unregisterMetrics(event.CfgName, "")
						delete(e.data, event.CfgName)
						gmStateInitalized = false
					} else {
						// Check if the index is within the slice bounds
						for indexToRemove, d := range e.data[event.CfgName] {
							if d.ProcessName == event.ProcessName {
								e.unregisterMetrics(event.CfgName, string(event.ProcessName))
								if indexToRemove < len(e.data[event.CfgName]) {
									e.data[event.CfgName] = append(e.data[event.CfgName][:indexToRemove], e.data[event.CfgName][indexToRemove+1:]...)
								}
							}
						}
					}
					continue
				}

				// Update the in MemData
				if _, ok := e.data[event.CfgName]; !ok {
					e.data[event.CfgName] = []Data{{
						ProcessName: event.ProcessName,
						State:       event.State,
						ClockType:   event.ClockType,
						IFace:       event.IFace,
						Metrics:     map[ValueType]DataMetrics{},
					}}
				} else {
					found := false
					for i, d := range e.data[event.CfgName] {
						if d.ProcessName == event.ProcessName {
							e.data[event.CfgName][i].State = event.State
							e.data[event.CfgName][i].IFace = event.IFace
							found = true
						}
					}
					if !found {
						e.data[event.CfgName] = append(e.data[event.CfgName], Data{
							ProcessName: event.ProcessName,
							State:       event.State,
							ClockType:   event.ClockType,
							Metrics:     map[ValueType]DataMetrics{},
							IFace:       event.IFace,
						})
					}
				}

				/// get Current GM state computing from DPLL, GNSS & ts2phc state
				gmState := e.getGMState(event.CfgName)
				if !e.stdoutToSocket { // if events not enabled
					if event.ProcessName != TS2PHCProcessName {
						e.updateMetrics(event.CfgName, event.ProcessName, event.Values)
						e.UpdateClockStateMetrics(event.State, string(event.ProcessName), event.IFace)
					}
					e.UpdateClockStateMetrics(gmState, string(GM), event.IFace)
				}
				logOut = append(logOut, fmt.Sprintf("%s[%d]:[%s] T-GM-STATUS %s\n", GM, time.Now().Unix(), event.CfgName, e.getGMState(event.CfgName)))
				if lastGmState != gmState || !gmStateInitalized {
					gmStateInitalized = true
					clockClass := e.updateCLockClass(event.CfgName, gmState, event.ClockType)
					logOut = append(logOut, fmt.Sprintf("%s[%d]:[%s] CLOCK_CLASS_CHANGE %d\n", PTP4l, time.Now().Unix(), event.CfgName, clockClass))
				}
				if event.WriteToLog {
					if e.stdoutToSocket {
						for _, l := range logOut {
							_, err := c.Write([]byte(l))
							if err != nil {
								glog.Errorf("Write error %s:", err)
								goto connect
							}
						}
					} else {
						for _, l := range logOut {
							fmt.Printf("%s", l)
						}
					}
				}
			case <-e.closeCh:
				return
			}
		}
		return
	}()
}

func (e *EventHandler) updateCLockClass(cfgName string, ptpState PTPState, clockType ClockType) (clockClass fbprotocol.ClockClass) {
	g, err := runGetGMSettings(cfgName)
	if err != nil {
		glog.Errorf("failed to get current GRANDMASTER_SETTINGS_NP: %s", err)
		return clockClass
	}
	glog.Infof("current GRANDMASTER_SETTINGS_NP:\n%s", g.String())
	switch ptpState {
	case PTP_LOCKED:
		switch clockType {
		case GM:
			// update only when ClockClass is changed
			if g.ClockQuality.ClockClass != fbprotocol.ClockClass6 {
				g.ClockQuality.ClockClass = fbprotocol.ClockClass6
				g.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyNanosecond100
				// T-REC-G.8275.1-202211-I section 6.3.5
				g.ClockQuality.OffsetScaledLogVariance = 0x4e5d
				runUpdateGMSettings(cfgName, g)
			}
		case OC:
		case BC:
		}
	case PTP_FREERUN:
		switch clockType {
		case GM:
			// update only when ClockClass is changed
			if g.ClockQuality.ClockClass != protocol.ClockClassFreerun {
				g.ClockQuality.ClockClass = protocol.ClockClassFreerun
				g.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
				// T-REC-G.8275.1-202211-I section 6.3.5
				g.ClockQuality.OffsetScaledLogVariance = 0xffff
				runUpdateGMSettings(cfgName, g)
			}
		case OC:
		case BC:
		}
	case PTP_HOLDOVER:
		switch clockType {
		case GM:
			// update only when ClockClass is changed
			if g.ClockQuality.ClockClass != fbprotocol.ClockClass7 {
				g.ClockQuality.ClockClass = fbprotocol.ClockClass7
				g.ClockQuality.ClockAccuracy = fbprotocol.ClockAccuracyUnknown
				// T-REC-G.8275.1-202211-I section 6.3.5
				g.ClockQuality.OffsetScaledLogVariance = 0xffff
				runUpdateGMSettings(cfgName, g)
			}
		case OC:
		case BC:
		}
	default:
	}
	return g.ClockQuality.ClockClass
}

func runGetGMSettings(cfgName string) (protocol.GrandmasterSettings, error) {
	cfgName = strings.Replace(cfgName, TS2PHCProcessName, PTP4lProcessName, 1)

	return pmc.RunPMCExpGetGMSettings(cfgName)
}

func runUpdateGMSettings(cfgName string, g protocol.GrandmasterSettings) {
	cfgName = strings.Replace(cfgName, TS2PHCProcessName, PTP4lProcessName, 1)

	err := pmc.RunPMCExpSetGMSettings(cfgName, g)
	if err != nil {
		glog.Errorf("failed to update GRANDMASTER_SETTINGS_NP: %s", err)
	}
}

// GetPTPState ...
func (e *EventHandler) GetPTPState(source EventSource, cfgName string) PTPState {
	if m, ok := e.data[cfgName]; ok {
		for _, v := range m {
			if v.ProcessName == source {
				return v.State
			}
		}
	}
	return PTP_UNKNOWN
}

func (e *EventHandler) listenToStateRequest() {
	for {
		select {
		case request := <-e.statusRequestChannel:
			if m, ok := e.data[request.CfgName]; ok {
				for _, v := range m {
					if v.ProcessName == request.Source {
						request.ResponseChannel <- v.State
					}
				}
			}
			request.ResponseChannel <- PTP_UNKNOWN
		}
	}
}

// UpdateClockStateMetrics ...
func (e *EventHandler) UpdateClockStateMetrics(state PTPState, process, iFace string) {
	labels := prometheus.Labels{}
	labels = prometheus.Labels{
		"process": process, "node": e.nodeName, "iface": iFace}
	if state == PTP_LOCKED {
		e.clockMetric.With(labels).Set(1)
	} else if state == PTP_FREERUN {
		e.clockMetric.With(labels).Set(0)
	} else if state == PTP_HOLDOVER {
		e.clockMetric.With(labels).Set(2)
	} else {
		e.clockMetric.With(labels).Set(3)
	}
}
func (e *EventHandler) updateMetrics(cfgName string, process EventSource, processData map[ValueType]int64) {
	if dataArray, ok := e.data[cfgName]; ok { //  create metric for the data that was captured
		for _, d := range dataArray {
			if d.ProcessName == process { // is gnss or dpll or ts2phc
				for dataType, value := range processData { // update process with metrics
					if _, found := d.Metrics[dataType]; !found { //todo: allow duplicate text
						if dataType == OFFSET {
							if d.Metrics[dataType].GaugeMetric == nil {
								m := d.Metrics[dataType]
								m.GaugeMetric = e.offsetMetric
								d.Metrics[dataType] = m
							}
							pLabels := map[string]string{"from": string(d.ProcessName), "node": e.nodeName,
								"process": string(d.ProcessName), "iface": d.IFace}
							d.Metrics[dataType].GaugeMetric.With(pLabels).Set(float64(value))
							continue
						} else {
							metrics := DataMetrics{
								isRegistered: true,
								GaugeMetric: prometheus.NewGaugeVec(
									prometheus.GaugeOpts{
										Namespace: PTPNamespace,
										Subsystem: PTPSubsystem,
										Name:      getMetricName(dataType),
										Help:      "",
									}, []string{"from", "node", "process", "iface"}),
								CounterMetric: nil,
								Name:          string(dataType),
								ValueType:     prometheus.GaugeValue,
								Labels: map[string]string{"from": string(d.ProcessName), "node": e.nodeName,
									"process": string(d.ProcessName), "iface": d.IFace},
								Value: float64(value),
							}
							registerMetrics(metrics.GaugeMetric)
							metrics.GaugeMetric.With(metrics.Labels).Set(float64(value))
							d.Metrics[dataType] = metrics
						}
					} else {
						s := d.Metrics[dataType]
						s.Labels = map[string]string{"from": string(d.ProcessName), "node": e.nodeName,
							"process": string(d.ProcessName), "iface": d.IFace}
						s.Value = float64(value)
						d.Metrics[dataType].GaugeMetric.With(s.Labels).Set(float64(value))
					}
				}
			}
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

func (e *EventHandler) unregisterMetrics(configName string, processName string) {
	if m, ok := e.data[configName]; ok {
		for _, v := range m {
			if string(v.ProcessName) == processName || processName == "" {
				for _, m := range v.Metrics {
					if m.GaugeMetric != nil {
						m.GaugeMetric.Delete(m.Labels)
					}
				}
			}
		}
	}
}

// GetPTPStateRequest ...
// GetPTPStateRequestChannel if Plugin requires to know the status of other compoenent they could use this  channel
// Send a status request
//
//	 responseChannel := make(chan string)
//	 statusRequestChannel <- StatusRequest{ResponseChannel: responseChannel}
//
//		Wait for and receive the response
//		response := <-responseChannel
func GetPTPStateRequest(request StatusRequest) {
	// Send a status request
	//responseChannel := make(chan string)
	statusRequestChannel <- request

	// Wait for and receive the response
	//response := <-responseChannel

}
func getMetricName(valueType ValueType) string {
	if strings.HasSuffix(string(valueType), string(OFFSET)) {
		return fmt.Sprintf("%s_%s", valueType, "ns")
	}
	return string(valueType)
}
