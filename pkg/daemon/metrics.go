package daemon

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/synce"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"

	"github.com/golang/glog"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilwait "k8s.io/apimachinery/pkg/util/wait"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	PTPNamespace       = "openshift"
	PTPSubsystem       = "ptp"
	GNSS               = "gnss"
	DPLL               = "dpll"
	ptp4lProcessName   = "ptp4l"
	phc2sysProcessName = "phc2sys"
	ts2phcProcessName  = "ts2phc"
	syncEProcessName   = "synce4l"
	clockRealTime      = "CLOCK_REALTIME"
	master             = "master"
	pmcSocketName      = "pmc"

	faultyOffset = 999999

	offset = "offset"
	rms    = "rms"

	// offset source
	phc = "phc"
	sys = "sys"
)

const (
	//LOCKED ...
	LOCKED string = "LOCKED"
	//FREERUN ...
	FREERUN = "FREERUN"
	// HOLDOVER
	HOLDOVER = "HOLDOVER"
)

const (
	PtpProcessDown int64 = 0
	PtpProcessUp   int64 = 1
)

type ptpPortRole int

const (
	PASSIVE ptpPortRole = iota
	SLAVE
	MASTER
	FAULTY
	UNKNOWN
	LISTENING
)

type masterOffsetInterface struct { // by slave iface with masked index
	sync.RWMutex
	iface map[string]ptpInterface
}
type ptpInterface struct {
	name  string
	alias string
}
type slaveInterface struct { // current slave iface name
	sync.RWMutex
	name map[string]string
}

type masterOffsetSourceProcess struct { // current slave iface name
	sync.RWMutex
	name map[string]string
}

var (
	masterOffsetIface  *masterOffsetInterface     // by slave iface with masked index
	slaveIface         *slaveInterface            // current slave iface name
	masterOffsetSource *masterOffsetSourceProcess // master offset source
	NodeName           = ""

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

var registerMetrics sync.Once

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

// InitializeOffsetMaps ... initialize maps
func InitializeOffsetMaps() {
	masterOffsetIface = &masterOffsetInterface{
		RWMutex: sync.RWMutex{},
		iface:   map[string]ptpInterface{},
	}
	slaveIface = &slaveInterface{
		RWMutex: sync.RWMutex{},
		name:    map[string]string{},
	}
	masterOffsetSource = &masterOffsetSourceProcess{
		RWMutex: sync.RWMutex{},
		name:    map[string]string{},
	}
}

// updatePTPMetrics ...
func updatePTPMetrics(from, process, iface string, ptpOffset, maxPtpOffset, frequencyAdjustment, delay float64) {
	Offset.With(prometheus.Labels{"from": from,
		"process": process, "node": NodeName, "iface": iface}).Set(ptpOffset)

	MaxOffset.With(prometheus.Labels{"from": from,
		"process": process, "node": NodeName, "iface": iface}).Set(maxPtpOffset)

	FrequencyAdjustment.With(prometheus.Labels{"from": from,
		"process": process, "node": NodeName, "iface": iface}).Set(frequencyAdjustment)

	Delay.With(prometheus.Labels{"from": from,
		"process": process, "node": NodeName, "iface": iface}).Set(delay)
}

// extractMetrics ...
func extractMetrics(messageTag string, processName string, ifaces config.IFaces, output string) (configName, source string, offset float64, state string, iface string) {
	configName = strings.Replace(strings.Replace(messageTag, "]", "", 1), "[", "", 1)
	if configName != "" {
		configName = strings.Split(configName, MessageTagSuffixSeperator)[0] // remove any suffix added to the configName
	}
	output = removeMessageSuffix(output)
	if strings.Contains(output, " max ") {
		ifaceName, ptpOffset, maxPtpOffset, frequencyAdjustment, delay := extractSummaryMetrics(configName, processName, output)
		if ifaceName != "" {
			if ifaceName == clockRealTime {
				updatePTPMetrics(phc, processName, ifaceName, ptpOffset, maxPtpOffset, frequencyAdjustment, delay)
			} else {
				updatePTPMetrics(master, processName, ifaceName, ptpOffset, maxPtpOffset, frequencyAdjustment, delay)
				masterOffsetSource.set(configName, processName)
			}
		}
	} else if strings.Contains(output, " offset ") {
		err, ifaceName, clockstate, ptpOffset, maxPtpOffset, frequencyAdjustment, delay := extractRegularMetrics(configName, processName, output, ifaces)
		if err != nil {
			glog.Error(err.Error())

		} else if ifaceName != "" {
			offsetSource := master
			if strings.Contains(output, "sys offset") {
				offsetSource = sys
			} else if strings.Contains(output, "phc offset") {
				offsetSource = phc
			}
			if offsetSource == master {
				masterOffsetSource.set(configName, processName)
			}
			updatePTPMetrics(offsetSource, processName, ifaceName, ptpOffset, maxPtpOffset, frequencyAdjustment, delay)
			updateClockStateMetrics(processName, ifaceName, clockstate)
		}
		source = processName
		offset = ptpOffset
		state = clockstate
		iface = ifaceName
	}
	if processName == ptp4lProcessName {
		if portId, role := extractPTP4lEventState(output); portId > 0 {
			if len(ifaces) >= portId-1 {
				UpdateInterfaceRoleMetrics(processName, ifaces[portId-1].Name, role)
				if role == SLAVE {
					masterOffsetIface.set(configName, ifaces[portId-1].Name)
					slaveIface.set(configName, ifaces[portId-1].Name)
				} else if role == FAULTY {
					if slaveIface.isFaulty(configName, ifaces[portId-1].Name) &&
						masterOffsetSource.get(configName) == ptp4lProcessName {
						updatePTPMetrics(master, processName, masterOffsetIface.get(configName).alias, faultyOffset, faultyOffset, 0, 0)
						updatePTPMetrics(phc, phc2sysProcessName, clockRealTime, faultyOffset, faultyOffset, 0, 0)
						updateClockStateMetrics(processName, masterOffsetIface.get(configName).alias, FREERUN)
						masterOffsetIface.set(configName, "")
						slaveIface.set(configName, "")
					}
				}
			}
		}
	}
	return
}

func extractSummaryMetrics(configName, processName, output string) (iface string, ptpOffset, maxPtpOffset, frequencyAdjustment, delay float64) {

	// phc2sys[5196755.139]: [ptp4l.0.config] ens5f0 rms 3152778 max 3152778 freq -6083928 +/-   0 delay  2791 +/-   0
	// phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms    4 max    4 freq -76829 +/-   0 delay  1085 +/-   0
	// ptp4l[74737.942]: [ptp4l.0.config] rms  53 max   74 freq -16642 +/-  40 delay  1089 +/-  20
	// or
	// ptp4l[365195.391]: [ptp4l.0.config] master offset         -1 s2 freq   -3972 path delay        89
	// ts2phc[82674.465]: [ts2phc.0.cfg] nmea delay: 88403525 ns
	// ts2phc[82674.465]: [ts2phc.0.cfg] ens2f1 extts index 0 at 1673031129.000000000 corr 0 src 1673031129.911642976 diff 0
	// ts2phc[82674.465]: [ts2phc.0.cfg] ens2f1 master offset          0 s2 freq      -0
	rmsIndex := strings.Index(output, rms)
	if rmsIndex < 0 {
		return
	}

	replacer := strings.NewReplacer("[", " ", "]", " ", ":", " ")
	output = replacer.Replace(output)

	indx := strings.Index(output, configName)
	if indx == -1 {
		return
	}
	output = output[indx:]
	fields := strings.Fields(output)

	// 0                1            2     3 4      5  6    7      8    9  10     11
	//ptp4l.0.config CLOCK_REALTIME rms   31 max   31 freq -77331 +/-   0 delay  1233 +/-   0
	if len(fields) < 8 {
		return
	}

	// when ptp4l log for master offset
	if fields[1] == rms { // if first field is rms , then add master
		fields = append(fields, "") // Making space for the new element
		//  0             1     2
		//ptp4l.0.config rms   53 max   74 freq -16642 +/-  40 delay  1089 +/-  20
		copy(fields[2:], fields[1:])                        // Shifting elements
		fields[1] = masterOffsetIface.get(configName).alias // Copying/inserting the value
		//  0             0       1   2
		//ptp4l.0.config master rms   53 max   74 freq -16642 +/-  40 delay  1089 +/-  20
	} else if fields[1] != "CLOCK_REALTIME" {
		// phc2sys[5196755.139]: [ptp4l.0.config] ens5f0 rms 3152778 max 3152778 freq -6083928 +/-   0 delay  2791 +/-   0
		return // do not register offset value for master port reported by phc2sys
	}

	iface = fields[1]

	ptpOffset, err := strconv.ParseFloat(fields[3], 64)
	if err != nil {
		glog.Errorf("%s failed to parse offset from the output %s error %v", processName, fields[3], err)
	}

	maxPtpOffset, err = strconv.ParseFloat(fields[5], 64)
	if err != nil {
		glog.Errorf("%s failed to parse max offset from the output %s error %v", processName, fields[5], err)
	}

	frequencyAdjustment, err = strconv.ParseFloat(fields[7], 64)
	if err != nil {
		glog.Errorf("%s failed to parse frequency adjustment output %s error %v", processName, fields[7], err)
	}

	if len(fields) >= 11 {
		delay, err = strconv.ParseFloat(fields[11], 64)
		if err != nil {
			glog.Errorf("%s failed to parse delay from the output %s error %v", processName, fields[11], err)
		}
	} else {
		// If there is no delay from master this mean we are out of sync
		glog.Warningf("no delay from master process %s out of sync", processName)
	}

	return
}

func extractRegularMetrics(configName, processName, output string, ifaces config.IFaces) (err error, iface, clockState string, ptpOffset, maxPtpOffset, frequencyAdjustment, delay float64) {
	indx := strings.Index(output, offset)
	if indx < 0 {
		return
	}

	output = strings.Replace(output, "path", "", 1)
	replacer := strings.NewReplacer("[", " ", "]", " ", ":", " ", " phc ", " ", " sys ", " ")
	output = replacer.Replace(output)

	index := strings.Index(output, configName)
	if index == -1 {
		return
	}

	output = output[index:]
	fields := strings.Fields(output)

	//       0         1      2          3     4   5    6          7     8
	// ptp4l.0.config master offset   -2162130 s2 freq +22451884  delay 374976
	// ts2phc.0.cfg  ens2f1  master    offset          0 s2 freq      -0
	// for multi card GNSS
	// ts2phc.0.cfg  /dev/ptp0  master    offset          0 s2 freq      -0
	// ts2phc.0.cfg  /dev/ptp4  master    offset          0 s2 freq      -0
	// ts2phc.0.cfg  /dev/ptp4 offset          0 s2 freq      -0
	// ts2phc.0.config /dev/ptp6 offset    0    s3 freq      +0 holdover
	// (ts2phc.0.cfg  master  offset      0    s2 freq     -0)
	if len(fields) < 7 {
		return
	}
	// linux 4.2 doesnt have master so we need to add that
	//    0                1      2              3
	//ts2phc.0.config  /dev/ptp6 offset          0 s2 freq      +0 (in linux 4.2)
	if processName == ts2phcProcessName {
		if fields[2] == offset || fields[3] == offset { // no master in log string
			fields[1] = ifaces.GetPhcID2IFace(fields[1])
			masterOffsetIface.set(configName, fields[1])
			slaveIface.set(configName, fields[1])
			if fields[2] == offset {
				fields[1] = master
			} else {
				copy(fields[1:], fields[2:])
				fields = fields[:len(fields)-1] // Truncate slice.
			}
			delay = 0
		}
	}
	//       0         1      2          3    4   5       6     7     8
	//ptp4l.0.config master offset       4    s2  freq   -3964 path delay  91
	// ts2phc.0.cfg  master  offset      0    s2  freq     -0
	if len(fields) < 7 {
		err = fmt.Errorf("%s failed to parse output %s: unexpected number of fields", processName, output)
		return
	}

	if fields[2] != offset {
		err = fmt.Errorf("%s failed to parse offset from the output %s error %s", processName, fields[1], "offset is not in right order")
		return
	}

	iface = fields[1]
	if iface != clockRealTime && iface != master {
		return // ignore master port offsets
	}

	if iface == master {
		iface = masterOffsetIface.get(configName).alias
	}

	ptpOffset, e := strconv.ParseFloat(fields[3], 64)
	if e != nil {
		err = fmt.Errorf("%s failed to parse offset from the output %s error %v", processName, fields[1], err)
		return
	}

	maxPtpOffset, err = strconv.ParseFloat(fields[3], 64)
	if err != nil {
		err = fmt.Errorf("%s failed to parse max offset from the output %s error %v", processName, fields[1], err)
		return
	}

	switch fields[4] {
	case "s0":
		clockState = FREERUN
	case "s1":
		clockState = FREERUN
	case "s2", "s3":
		clockState = LOCKED
	default:
		clockState = FREERUN
	}
	// TS@PHC has holdover state when it is out of sync
	if processName == ts2phcProcessName && strings.Contains(output, "holdover") {
		clockState = HOLDOVER
	}

	frequencyAdjustment, err = strconv.ParseFloat(fields[6], 64)
	if err != nil {
		err = fmt.Errorf("%s failed to parse frequency adjustment output %s error %v", processName, fields[6], err)
		return
	}

	if processName == ts2phcProcessName {
		// ts2phc acts as GM so no path delay
		delay = 0
	} else if len(fields) > 8 {
		delay, err = strconv.ParseFloat(fields[8], 64)
		if err != nil {
			err = fmt.Errorf("%s failed to parse delay from the output %s error %v", processName, fields[8], err)
		}
	} else {
		// If there is no delay this mean we are out of sync
		glog.Warningf("no delay from the process %s out of sync", processName)
	}

	return
}

// updateClockStateMetrics ...
func updateClockStateMetrics(process, iface string, state string) {
	if state == LOCKED {
		ClockState.With(prometheus.Labels{
			"process": process, "node": NodeName, "iface": iface}).Set(1)
	} else {
		ClockState.With(prometheus.Labels{
			"process": process, "node": NodeName, "iface": iface}).Set(0)
	}
}

func UpdateInterfaceRoleMetrics(process string, iface string, role ptpPortRole) {
	InterfaceRole.With(prometheus.Labels{
		"process": process, "node": NodeName, "iface": iface}).Set(float64(role))
}

// UpdateClockClassMetrics ... update clock class metrics
func UpdateClockClassMetrics(clockClass float64) {
	ClockClassMetrics.With(prometheus.Labels{
		"process": ptp4lProcessName, "node": NodeName}).Set(float64(clockClass))
}

func UpdateProcessStatusMetrics(process, cfgName string, status int64) {
	ProcessStatus.With(prometheus.Labels{
		"process": process, "node": NodeName, "config": cfgName}).Set(float64(status))
	if status == PtpProcessUp {
		ProcessRestartCount.With(prometheus.Labels{
			"process": process, "node": NodeName, "config": cfgName}).Inc()
	}
}

// UpdatePTPHAMetrics ... update ptp ha  metrics
func UpdatePTPHAMetrics(profile string, inActiveProfiles []string, state int64) {
	PTPHAMetrics.With(prometheus.Labels{
		"process": phc2sysProcessName, "node": NodeName, "profile": profile}).Set(float64(state))
	for _, inActive := range inActiveProfiles {
		PTPHAMetrics.With(prometheus.Labels{
			"process": phc2sysProcessName, "node": NodeName, "profile": inActive}).Set(0)
	}
}

func UpdateSynceClockQlMetrics(process, cfgName string, iface string, network_option int, device string, value int) {
	SynceClockQL.With(prometheus.Labels{
		"process": process, "node": NodeName, "profile": cfgName, "network_option": strconv.Itoa(network_option), "iface": iface, "device": device}).Set(float64(value))
}

func UpdateSynceQLMetrics(process, cfgName string, iface string, network_option int, device string, qlType string, value byte) {
	SynceQLInfo.With(prometheus.Labels{
		"process": process, "node": NodeName, "profile": cfgName, "iface": iface,
		"network_option": strconv.Itoa(network_option), "device": device, "ql_type": qlType}).Set(float64(value))

}
func deleteSyncEMetrics(process, configName string, relations *synce.Relations) {
	for _, device := range relations.Devices {
		for _, iface := range device.Ifaces {
			SynceQLInfo.Delete(prometheus.Labels{
				"process": process, "node": NodeName, "profile": configName, "iface": iface, "device": device.Name, "network_option": strconv.Itoa(device.NetworkOption), "ql_type": "SSM"})
			SynceQLInfo.Delete(prometheus.Labels{
				"process": process, "node": NodeName, "profile": configName, "iface": iface, "device": device.Name, "network_option": strconv.Itoa(device.NetworkOption), "ql_type": "Extended SSM"})

			SynceClockQL.Delete(prometheus.Labels{
				"process": process, "node": NodeName, "profile": configName, "iface": iface, "device": device.Name, "network_option": strconv.Itoa(device.NetworkOption)})

			ClockState.Delete(prometheus.Labels{
				"process": process, "node": NodeName, "iface": iface})
		}
	}
}

// DeleteMetrics ... update ptp ha  metrics
func deleteMetrics(ifaces config.IFaces, haProfiles map[string][]string, process, config string) {
	if process == phc2sysProcessName {
		deleteOsClockStateMetrics(haProfiles)
		return
	}
	deleteProcessStatusMetrics(config, process)
	for _, iface := range ifaces {
		InterfaceRole.Delete(prometheus.Labels{
			"process": ptp4lProcessName, "node": NodeName, "iface": iface.Name})
	}
	for _, iface := range masterOffsetIface.iface {
		ClockState.Delete(prometheus.Labels{
			"process": process, "node": NodeName, "iface": iface.alias})
		Delay.Delete(prometheus.Labels{
			"from": master, "process": process, "node": NodeName, "iface": iface.alias})
		FrequencyAdjustment.Delete(prometheus.Labels{
			"from": master, "process": process, "node": NodeName, "iface": iface.alias})
		MaxOffset.Delete(prometheus.Labels{
			"from": master, "process": process, "node": NodeName, "iface": iface.alias})
		Offset.Delete(prometheus.Labels{
			"from": master, "process": process, "node": NodeName, "iface": iface.alias})
	}
}

func deleteOsClockStateMetrics(profiles map[string][]string) {
	ClockState.Delete(prometheus.Labels{
		"process": phc2sysProcessName, "node": NodeName, "iface": clockRealTime})
	Delay.Delete(prometheus.Labels{
		"from": phc, "process": phc2sysProcessName, "node": NodeName, "iface": clockRealTime})
	FrequencyAdjustment.Delete(prometheus.Labels{
		"from": phc, "process": phc2sysProcessName, "node": NodeName, "iface": clockRealTime})
	MaxOffset.Delete(prometheus.Labels{
		"from": phc, "process": phc2sysProcessName, "node": NodeName, "iface": clockRealTime})
	Offset.Delete(prometheus.Labels{
		"from": phc, "process": phc2sysProcessName, "node": NodeName, "iface": clockRealTime})
	for profile := range profiles {
		PTPHAMetrics.Delete(prometheus.Labels{
			"process": phc2sysProcessName, "node": NodeName, "profile": profile})
	}
}

func deleteProcessStatusMetrics(config, process string) {
	ProcessStatus.Delete(prometheus.Labels{
		"process": process, "node": NodeName, "config": config})
	ProcessRestartCount.Delete(prometheus.Labels{
		"process": process, "node": NodeName, "config": config})

}
func extractPTP4lEventState(output string) (portId int, role ptpPortRole) {
	replacer := strings.NewReplacer("[", " ", "]", " ", ":", " ")
	output = replacer.Replace(output)

	//ptp4l 4268779.809 ptp4l.o.config port 2: LISTENING to PASSIVE on RS_PASSIVE
	//ptp4l 4268779.809 ptp4l.o.config port 1: delay timeout
	index := strings.Index(output, " port ")
	if index == -1 {
		return
	}

	output = output[index:]
	fields := strings.Fields(output)

	//port 1: delay timeout
	if len(fields) < 2 {
		glog.Errorf("failed to parse output %s: unexpected number of fields", output)
		return
	}

	portIndex := fields[1]
	role = UNKNOWN

	var e error
	portId, e = strconv.Atoi(portIndex)
	if e != nil {
		glog.Errorf("error parsing port id %s", e)
		portId = 0
		return
	}

	if strings.Contains(output, "UNCALIBRATED to SLAVE") {
		role = SLAVE
	} else if strings.Contains(output, "UNCALIBRATED to PASSIVE") || strings.Contains(output, "MASTER to PASSIVE") ||
		strings.Contains(output, "SLAVE to PASSIVE") {
		role = PASSIVE
	} else if strings.Contains(output, "UNCALIBRATED to MASTER") || strings.Contains(output, "LISTENING to MASTER") {
		role = MASTER
	} else if strings.Contains(output, "FAULT_DETECTED") || strings.Contains(output, "SYNCHRONIZATION_FAULT") {
		role = FAULTY
	} else if strings.Contains(output, "UNCALIBRATED to LISTENING") || strings.Contains(output, "SLAVE to LISTENING") ||
		strings.Contains(output, "INITIALIZING to LISTENING") {
		role = LISTENING
	} else {
		portId = 0
	}
	return
}

func addFlagsForMonitor(process string, configOpts *string, conf *Ptp4lConf, stdoutToSocket bool) {
	switch process {
	case "ptp4l":
		// If output doesn't exist we add it for the prometheus exporter
		if configOpts != nil {
			if !strings.Contains(*configOpts, "-m") {
				glog.Info("adding -m to print messages to stdout for ptp4l to use prometheus exporter")
				*configOpts = fmt.Sprintf("%s -m", *configOpts)
			}

			if !strings.Contains(*configOpts, "--summary_interval") {
				_, exist := conf.getPtp4lConfOptionOrEmptyString(GlobalSectionName, "summary_interval")
				if !exist {
					conf.setPtp4lConfOption(GlobalSectionName, "summary_interval", "1", true)
				}
			}
		}
	case "phc2sys":
		// If output doesn't exist we add it for the prometheus exporter
		if configOpts != nil && *configOpts != "" {
			if !strings.Contains(*configOpts, "-m") {
				glog.Info("adding -m to print messages to stdout for phc2sys to use prometheus exporter")
				*configOpts = fmt.Sprintf("%s -m", *configOpts)
			}
			// stdoutToSocket is for sidecar to consume events, -u  will not generate logs with offset and clock state.
			// disable -u for  events
			if stdoutToSocket && strings.Contains(*configOpts, "-u") {
				glog.Error("-u option will not generate clock state events,  remove -u option")
			} else if !stdoutToSocket && !strings.Contains(*configOpts, "-u") {
				glog.Info("adding -u 1 to print summary messages to stdout for phc2sys to use prometheus exporter")
				*configOpts = fmt.Sprintf("%s -u 1", *configOpts)
			}
		}
	case "ts2phc":
	}

}

// StartMetricsServer runs the prometheus listner so that metrics can be collected
func StartMetricsServer(bindAddress string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	go utilwait.Until(func() {
		err := http.ListenAndServe(bindAddress, mux)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("starting metrics server failed: %v", err))
		}
	}, 5*time.Second, utilwait.NeverStop)
}

func (m *masterOffsetInterface) get(configName string) ptpInterface {
	m.RLock()
	defer m.RUnlock()
	if s, found := m.iface[configName]; found {
		return s
	}
	return ptpInterface{
		name:  "",
		alias: "",
	}
}
func (m *masterOffsetInterface) getByAlias(configName string, alias string) ptpInterface {
	m.RLock()
	defer m.RUnlock()
	if s, found := m.iface[configName]; found {
		if s.alias == alias {
			return s
		}
	}
	return ptpInterface{
		name:  alias,
		alias: alias,
	}
}

func (m *masterOffsetInterface) getAliasByName(configName string, name string) ptpInterface {
	if name == clockRealTime || name == master {
		return ptpInterface{
			name:  name,
			alias: name,
		}
	}
	m.RLock()
	defer m.RUnlock()
	if s, found := m.iface[configName]; found {
		if s.name == name {
			return s
		}
	}
	return ptpInterface{
		name:  name,
		alias: name,
	}
}

func (m *masterOffsetInterface) set(configName string, value string) {
	m.Lock()
	defer m.Unlock()
	m.iface[configName] = ptpInterface{
		name:  value,
		alias: utils.GetAlias(value),
	}
}

func (s *slaveInterface) set(configName string, value string) {
	s.Lock()
	defer s.Unlock()
	s.name[configName] = value
}

func (s *slaveInterface) isFaulty(configName string, iface string) bool {
	s.RLock()
	defer s.RUnlock()

	if si, found := s.name[configName]; found {
		if si == iface {
			return true
		}
	}
	return false
}

func (mp *masterOffsetSourceProcess) set(configName string, value string) {
	mp.Lock()
	defer mp.Unlock()
	mp.name[configName] = value
}

func (mp *masterOffsetSourceProcess) get(configName string) string {
	if s, found := mp.name[configName]; found {
		return s
	}
	return ptp4lProcessName // default is ptp4l
}
