package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"strconv"
)

var NodeName string // to be initialized on startup or via setter

// UpdateClockStateMetrics sets the ClockState metric (1 = LOCKED, 0 = other)
func UpdateClockStateMetrics(process, iface, state string) {
	val := 0.0
	if state == "LOCKED" {
		val = 1.0
	}
	ClockState.With(prometheus.Labels{"process": process, "node": NodeName, "iface": iface}).Set(val)
}

func UpdateInterfaceRoleMetrics(process, iface string, role int) {
	InterfaceRole.With(prometheus.Labels{"process": process, "node": NodeName, "iface": iface}).Set(float64(role))
}

func UpdateClockClassMetrics(clockClass float64) {
	ClockClassMetrics.With(prometheus.Labels{"process": "ptp4l", "node": NodeName}).Set(clockClass)
}

func UpdateProcessStatusMetrics(process, cfgName string, status int64) {
	ProcessStatus.With(prometheus.Labels{
		"process": process, "node": NodeName, "config": cfgName}).Set(float64(status))

	if status == 1 { // Assuming 1 == PtpProcessUp
		ProcessRestartCount.With(prometheus.Labels{
			"process": process, "node": NodeName, "config": cfgName}).Inc()
	}
}

func UpdatePTPHAMetrics(profile string, inActiveProfiles []string, state int64) {
	PTPHAMetrics.With(prometheus.Labels{
		"process": "phc2sys", "node": NodeName, "profile": profile}).Set(float64(state))

	for _, inActive := range inActiveProfiles {
		PTPHAMetrics.With(prometheus.Labels{
			"process": "phc2sys", "node": NodeName, "profile": inActive}).Set(0)
	}
}

func UpdateSynceClockQlMetrics(process, cfgName, iface string, networkOption int, device string, value int) {
	SynceClockQL.With(prometheus.Labels{
		"process": process, "node": NodeName, "profile": cfgName, "network_option": strconv.Itoa(networkOption),
		"iface": iface, "device": device}).Set(float64(value))
}

func UpdateSynceQLMetrics(process, cfgName, iface string, networkOption int, device, qlType string, value byte) {
	SynceQLInfo.With(prometheus.Labels{
		"process": process, "node": NodeName, "profile": cfgName, "iface": iface,
		"network_option": strconv.Itoa(networkOption), "device": device, "ql_type": qlType}).Set(float64(value))
}

// UpdatePTPMetrics updatePTPMetrics ...
func UpdatePTPMetrics(from, process, iface string, ptpOffset, maxPtpOffset, frequencyAdjustment, delay float64) {
	Offset.With(prometheus.Labels{"from": from,
		"process": process, "node": NodeName, "iface": iface}).Set(ptpOffset)

	MaxOffset.With(prometheus.Labels{"from": from,
		"process": process, "node": NodeName, "iface": iface}).Set(maxPtpOffset)

	FrequencyAdjustment.With(prometheus.Labels{"from": from,
		"process": process, "node": NodeName, "iface": iface}).Set(frequencyAdjustment)

	Delay.With(prometheus.Labels{"from": from,
		"process": process, "node": NodeName, "iface": iface}).Set(delay)
}
