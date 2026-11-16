package metrics

import (
	"strconv"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/synce"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	clockRealtime      = "CLOCK_REALTIME"
	phcSource          = "phc"
	labelProcess       = "process"
	labelNode          = "node"
	labelIface         = "iface"
	labelDevice        = "device"
	labelNetworkOption = "network_option"
	labelConfig        = "config"
	labelQLType        = "ql_type"
	labelFrom          = "from"
	labelProfile       = "profile"
)

// DeleteSynceMetrics ...
func DeleteSynceMetrics(process, configName string, relations *synce.Relations) {
	for _, device := range relations.Devices {
		for _, iface := range device.Ifaces {
			for _, qlType := range []string{"SSM", "Extended SSM"} {
				SynceQLInfo.Delete(prometheus.Labels{
					labelProcess: process, labelNode: NodeName, labelProfile: configName,
					labelIface: iface, labelDevice: device.Name, labelNetworkOption: strconv.Itoa(device.NetworkOption),
					labelQLType: qlType})
			}
			SynceClockQL.Delete(prometheus.Labels{
				labelProcess: process, labelNode: NodeName, labelProfile: configName,
				labelIface: iface, labelDevice: device.Name, labelNetworkOption: strconv.Itoa(device.NetworkOption)})
			ClockState.Delete(prometheus.Labels{labelProcess: process, labelNode: NodeName, labelIface: iface})
		}
	}
}

// DeleteMetrics ...
func DeleteMetrics(ifaces config.IFaces, haProfiles map[string][]string, process, config string) {
	if process == event.Phc2SysProcessName {
		deleteOsClockStateMetrics(haProfiles)
		return
	}

	DeleteProcessStatusMetrics(config, process)
	for _, iface := range ifaces {
		InterfaceRole.Delete(prometheus.Labels{labelProcess: event.Ptp4lProcessName, labelNode: NodeName, labelIface: iface.Name})
	}
	// You may also delete Offset/Freq/Delay/MaxOffset for known ifaces as needed
}

func deleteOsClockStateMetrics(profiles map[string][]string) {
	ClockState.Delete(prometheus.Labels{labelProcess: event.Phc2SysProcessName, labelNode: NodeName, labelIface: clockRealtime})
	Delay.Delete(prometheus.Labels{labelFrom: phcSource, labelProcess: event.Phc2SysProcessName, labelNode: NodeName, labelIface: clockRealtime})
	FrequencyAdjustment.Delete(prometheus.Labels{labelFrom: phcSource, labelProcess: event.Phc2SysProcessName, labelNode: NodeName, labelIface: clockRealtime})
	MaxOffset.Delete(prometheus.Labels{labelFrom: phcSource, labelProcess: event.Phc2SysProcessName, labelNode: NodeName, labelIface: clockRealtime})
	Offset.Delete(prometheus.Labels{labelFrom: phcSource, labelProcess: event.Phc2SysProcessName, labelNode: NodeName, labelIface: clockRealtime})

	for profile := range profiles {
		PTPHAMetrics.Delete(prometheus.Labels{labelProcess: event.Phc2SysProcessName, labelNode: NodeName, labelProfile: profile})
	}
}

// DeleteProcessStatusMetrics ...
func DeleteProcessStatusMetrics(config, process string) {
	ProcessStatus.Delete(prometheus.Labels{labelProcess: process, labelNode: NodeName, labelConfig: config})
	ProcessRestartCount.Delete(prometheus.Labels{labelProcess: process, labelNode: NodeName, labelConfig: config})
}
