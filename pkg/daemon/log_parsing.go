package daemon

import (
	"strings"
	"time"

	v1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"

	"github.com/golang/glog"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

func convertParserRoleToMetricsRole(role parserconstants.PTPPortRole) event.PtpPortRole {
	switch role {
	case parserconstants.PortRoleSlave:
		return event.SLAVE
	case parserconstants.PortRoleMaster:
		return event.MASTER
	case parserconstants.PortRolePassive:
		return event.PASSIVE
	case parserconstants.PortRoleFaulty:
		return event.FAULTY
	case parserconstants.PortRoleListening:
		return event.LISTENING
	default:
		return event.UNKNOWN
	}
}

func convertParserClockStateEventPTPState(clockState parserconstants.ClockState) event.PTPState {
	switch clockState {
	case parserconstants.ClockStateFreeRun:
		return event.PTP_FREERUN
	case parserconstants.ClockStateLocked:
		return event.PTP_LOCKED
	case parserconstants.ClockStateHoldover:
		return event.PTP_HOLDOVER
	}
	return event.PTP_NOTSET
}

func getParser(processName string) parser.MetricsExtractor {
	switch processName {
	case ptp4lProcessName:
		return parser.NewPTP4LExtractor()
	case phc2sysProcessName:
		return parser.NewPhc2SysExtractor()
	case ts2phcProcessName:
		return parser.NewTS2PHCExtractor()
	default:
		glog.Errorf("No parser available for process: %s", processName)
		return nil
	}
}

// processWithParser uses the new parser-based approach for processes with parsers
func processWithParser(process *ptpProcess, output string) {
	// Extract metrics and events using the parser
	metrics, ptpEvent, err := process.logParser.Extract(output)
	if err != nil {
		glog.Errorf("Failed to extract metrics from %s output: %v", process.name, err)
		return
	}

	process.hasCollectedMetrics = true

	// Process metrics if available
	if metrics != nil {
		processParsedMetrics(process, metrics)
	}

	// Process PTP events if available
	if ptpEvent != nil {
		processParsedEvent(process, ptpEvent)
	}
}

func processParsedMetrics(process *ptpProcess, ptpMetrics *parser.Metrics) {
	// Convert interface from possible clock id
	iface := process.ifaces.GetPhcID2IFace(ptpMetrics.Iface)
	if iface != clockRealTime {
		iface = utils.GetAlias(iface)
	}

	// Update PTP metrics using the parsed data
	updatePTPMetrics(ptpMetrics.Source, process.name, iface, ptpMetrics.Offset, ptpMetrics.MaxOffset, ptpMetrics.FreqAdj, ptpMetrics.Delay)

	// Update clock state metrics if available
	if ptpMetrics.ClockState != "" {
		updateClockStateMetrics(process.name, iface, string(ptpMetrics.ClockState))
	}

	configName := strings.Replace(strings.Replace(process.messageTag, "]", "", 1), "[", "", 1)
	if configName != "" {
		configName = strings.Split(configName, MessageTagSuffixSeperator)[0]
	}

	// Handle master offset source tracking
	if ptpMetrics.Source == "master" && configName != "" {
		masterOffsetSource.set(configName, process.name)
	}

	// if state is HOLDOVER do not update the state
	state := convertParserClockStateEventPTPState(ptpMetrics.ClockState)

	// transition to FREERUN if offset is outside configured thresholds
	if shouldFreeRun(state, ptpMetrics.Offset, process.ptpClockThreshold) {
		state = event.PTP_FREERUN
	}

	switch process.name {
	case ptp4lProcessName:
		if ptpMetrics.Iface != "" && configName != "" {
			masterOffsetIface.set(configName, ptpMetrics.Iface)
		}
	case ts2phcProcessName:
		// Send event for ts2phc
		eventSource := process.ifaces.GetEventSource(process.ifaces.GetPhcID2IFace(ptpMetrics.Iface))
		values := map[event.ValueType]interface{}{
			event.OFFSET: int64(ptpMetrics.Offset),
		}
		if eventSource == event.GNSS {
			values[event.NMEA_STATUS] = int64(1)
		}
		select {
		case process.eventCh <- event.EventChannel{
			ProcessName: event.TS2PHC,
			State:       state,
			CfgName:     configName,
			IFace:       iface,
			Values:      values,
			ClockType:   process.clockType,
			Time:        time.Now().UnixMilli(),
			WriteToLog:  eventSource == event.GNSS,
			Reset:       false,
		}:
		default:
		}
	}
}

// processParsedEvent handles PTP events extracted by the parser
func processParsedEvent(process *ptpProcess, ptpEvent *parser.PTPEvent) {
	if process.name != ptp4lProcessName {
		return
	}

	// Update interface role metrics
	if ptpEvent.PortID > 0 && len(process.ifaces) >= ptpEvent.PortID-1 {
		configName := strings.Replace(strings.Replace(process.messageTag, "]", "", 1), "[", "", 1)
		configName = strings.Split(configName, MessageTagSuffixSeperator)[0]

		interfaceName := process.ifaces[ptpEvent.PortID-1].Name
		role := convertParserRoleToMetricsRole(ptpEvent.Role)
		UpdateInterfaceRoleMetrics(process.name, interfaceName, role)
		process.handler.SetPortRole(configName, interfaceName, ptpEvent)

		if configName == "" {
			return
		}

		// Handle role-specific logic
		switch ptpEvent.Role {
		case parserconstants.PortRoleSlave:
			masterOffsetIface.set(configName, interfaceName)
			slaveIface.set(configName, interfaceName)
		case parserconstants.PortRoleFaulty:
			isFaulty := slaveIface.isFaulty(configName, interfaceName)
			sourceIsPtp4l := masterOffsetSource.get(configName) == ptp4lProcessName
			if isFaulty && sourceIsPtp4l {
				// Set fault metrics and clear slave & master offset interfaces
				updatePTPMetrics(master, process.name, masterOffsetIface.get(configName).alias, faultyOffset, faultyOffset, 0, 0)
				updatePTPMetrics(phc, phc2sysProcessName, clockRealTime, faultyOffset, faultyOffset, 0, 0)
				updateClockStateMetrics(process.name, masterOffsetIface.get(configName).alias, FREERUN)
				masterOffsetIface.set(configName, "")
				slaveIface.set(configName, "")
			}
		}
	}
}

// shouldFreeRun returns true if we’re not already in HOLDOVER or FREERUN
// and the current offset breaches either threshold.
func shouldFreeRun(
	currentState event.PTPState,
	rawOffset float64,
	th *v1.PtpClockThreshold,
) bool {
	if currentState == event.PTP_HOLDOVER || currentState == event.PTP_FREERUN {
		return false
	}

	ofs := int64(rawOffset)
	return ofs >= th.MaxOffsetThreshold || ofs <= th.MinOffsetThreshold
}
