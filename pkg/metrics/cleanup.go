package metrics

import (
	"strconv"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/synce"
	"github.com/prometheus/client_golang/prometheus"
)

// DeleteSynceMetrics ...
func DeleteSynceMetrics(process, configName string, relations *synce.Relations) {
	for _, device := range relations.Devices {
		for _, iface := range device.Ifaces {
			for _, qlType := range []string{"SSM", "Extended SSM"} {
				SynceQLInfo.Delete(prometheus.Labels{
					"process": process, "node": NodeName, "profile": configName,
					"iface": iface, "device": device.Name, "network_option": strconv.Itoa(device.NetworkOption),
					"ql_type": qlType})
			}
			SynceClockQL.Delete(prometheus.Labels{
				"process": process, "node": NodeName, "profile": configName,
				"iface": iface, "device": device.Name, "network_option": strconv.Itoa(device.NetworkOption)})
			ClockState.Delete(prometheus.Labels{"process": process, "node": NodeName, "iface": iface})
		}
	}
}

// DeleteMetrics ...
func DeleteMetrics(ifaces config.IFaces, haProfiles map[string][]string, process, config string) {
	if process == "phc2sys" {
		deleteOsClockStateMetrics(haProfiles)
		return
	}

	DeleteProcessStatusMetrics(config, process)
	for _, iface := range ifaces {
		InterfaceRole.Delete(prometheus.Labels{"process": "ptp4l", "node": NodeName, "iface": iface.Name})
	}
	// You may also delete Offset/Freq/Delay/MaxOffset for known ifaces as needed
}

func deleteOsClockStateMetrics(profiles map[string][]string) {
	ClockState.Delete(prometheus.Labels{"process": "phc2sys", "node": NodeName, "iface": "CLOCK_REALTIME"})
	Delay.Delete(prometheus.Labels{"from": "phc", "process": "phc2sys", "node": NodeName, "iface": "CLOCK_REALTIME"})
	FrequencyAdjustment.Delete(prometheus.Labels{"from": "phc", "process": "phc2sys", "node": NodeName, "iface": "CLOCK_REALTIME"})
	MaxOffset.Delete(prometheus.Labels{"from": "phc", "process": "phc2sys", "node": NodeName, "iface": "CLOCK_REALTIME"})
	Offset.Delete(prometheus.Labels{"from": "phc", "process": "phc2sys", "node": NodeName, "iface": "CLOCK_REALTIME"})

	for profile := range profiles {
		PTPHAMetrics.Delete(prometheus.Labels{"process": "phc2sys", "node": NodeName, "profile": profile})
	}
}

// DeleteProcessStatusMetrics ...
func DeleteProcessStatusMetrics(config, process string) {
	ProcessStatus.Delete(prometheus.Labels{"process": process, "node": NodeName, "config": config})
	ProcessRestartCount.Delete(prometheus.Labels{"process": process, "node": NodeName, "config": config})
}
