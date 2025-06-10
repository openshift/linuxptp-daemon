package parser

import (
	"fmt"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	sstate "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/state"
	"regexp"
	"strings"
)

var (
	ptp4lEventRegex = regexp.MustCompile(`ptp4l\[\d+\.\d+\]:\s+\[.*?\]\s+port\s+(?P<port>\d+):\s+(?P<event>.+)`)
	// ptp4l rms regex
	summaryRegex = regexp.MustCompile(`^ptp4l\[\d+\.\d+\]:\s+\[.*\.\d+\.config\]\s(?P<interface>\w+)?\s*rms\s+(?P<rms>\d+)\s+max\s+(?P<max>-?\d+)\s+freq\s+(?P<freq_adj>[-+]\d+)\s+\+/-\s+\d+\s*(?:delay\s+(?P<delay>\d+)\s+\+/-\s+\d+)?$`)
	// ptp4l master offset regex
	regularRegex = regexp.MustCompile(`^ptp4l\[\d+\.\d+\]:\s+\[.*\.\d+\.config\]\s(?P<interface>\w+)?\s+offset\s+(?P<offset>-?\d+)\s+(?P<state>s\d)\s+freq\s+(?P<freq_adj>[-+]\d+)\s*(?:path\s+delay\s+(?P<delay>\d+))?$`)
)

// NewPTP4LExtractor creates a new PTP4LExtractor.
func NewPTP4LExtractor(state *sstate.SharedState) *BaseMetricsExtractor {
	return &BaseMetricsExtractor{
		ProcessNameStr: constants.PTP4L,
		ExtractSummaryFn: func(logLine string) (*Metrics, error) {
			return extractSummaryPTP4l(logLine)
		},
		ExtractRegularFn: func(logLine string) (*Metrics, error) {
			return extractRegularPTP4l(logLine)
		},
		ExtraEventFn: func(output string) (*PTPEvent, error) {
			return extractEventPTP4l(output)
		},
		State: state,
	}
}

func extractEventPTP4l(output string) (*PTPEvent, error) {
	if output == "" {
		return nil, fmt.Errorf("empty output")
	}

	if !strings.Contains(output, " port ") {
		return nil, fmt.Errorf("invalid log for parsing events")
	}

	groups := extractNamedGroups(ptp4lEventRegex, output)
	if groups == nil {
		return nil, fmt.Errorf("regex did not match: %s", output)
	}

	portId, err := validateInt(groups["port"], "port")
	if err != nil {
		return nil, err
	}

	role, err := determineRole(groups["event"])
	if err != nil {
		portId = 0
	}

	return &PTPEvent{
		PortID: portId,
		Role:   role,
		Raw:    output,
	}, err
}

func extractSummaryPTP4l(output string) (*Metrics, error) {
	if output == "" {
		return nil, fmt.Errorf("empty output")
	}

	match := summaryRegex.FindStringSubmatch(output)
	if match == nil {
		return nil, fmt.Errorf("no match found for summary line: %s", output)
	}

	groupNames := summaryRegex.SubexpNames()
	results := map[string]string{}
	for i, name := range groupNames {
		if i != 0 && name != "" {
			results[name] = match[i]
		}
	}

	rmsOffset, err := validateFloat(results["rms"], "rms offset")
	if err != nil {
		return nil, err
	}

	maxOffset, err := validateFloat(results["max"], "max offset")
	if err != nil {
		return nil, err
	}

	freqAdj, err := validateFloat(results["freq_adj"], "frequency adjustment")
	if err != nil {
		return nil, err
	}

	delay, err := validateFloat(results["delay"], "delay")
	if err != nil {
		// Delay is optional, set to 0 if not present
		delay = 0
	}

	iface := results["interface"]
	if iface == "" {
		iface = master
	}

	return &Metrics{
		Iface:     iface,
		Offset:    rmsOffset,
		MaxOffset: maxOffset,
		FreqAdj:   freqAdj,
		Delay:     delay,
		Source:    master,
	}, nil
}

func extractRegularPTP4l(output string) (*Metrics, error) {
	if output == "" {
		return nil, fmt.Errorf("empty output")
	}

	match := regularRegex.FindStringSubmatch(output)
	if match == nil {
		return nil, fmt.Errorf("no match found for regular line: %s", output)
	}

	groupNames := regularRegex.SubexpNames()
	results := map[string]string{}
	for i, name := range groupNames {
		if i != 0 && name != "" {
			results[name] = match[i]
		}
	}

	if results["interface"] != master {
		return nil, fmt.Errorf("non-master interface: %s", results["interface"])
	}

	ptpOffset, err := validateFloat(results["offset"], "offset")
	if err != nil {
		return nil, err
	}

	freqAdj, err := validateFloat(results["freq_adj"], "frequency adjustment")
	if err != nil {
		return nil, err
	}

	delay, err := validateFloat(results["delay"], "delay")
	if err != nil {
		// Delay is optional, set to 0 if not present
		delay = 0
	}

	clockState := parseClockState(results["state"])

	return &Metrics{
		Iface:      results["interface"],
		Offset:     ptpOffset,
		MaxOffset:  ptpOffset,
		FreqAdj:    freqAdj,
		Delay:      delay,
		ClockState: clockState,
		Source:     master,
	}, nil
}

func determineRole(event string) (PTPPortRole, error) {
	switch {
	case strings.Contains(event, "UNCALIBRATED to SLAVE"):
		return SLAVE, nil
	case strings.Contains(event, "UNCALIBRATED to PASSIVE"), strings.Contains(event, "MASTER to PASSIVE"), strings.Contains(event, "SLAVE to PASSIVE"):
		return PASSIVE, nil
	case strings.Contains(event, "UNCALIBRATED to MASTER"), strings.Contains(event, "LISTENING to MASTER"):
		return MASTER, nil
	case strings.Contains(event, "FAULT_DETECTED"), strings.Contains(event, "SYNCHRONIZATION_FAULT"):
		return FAULTY, nil
	case strings.Contains(event, "UNCALIBRATED to LISTENING"), strings.Contains(event, "SLAVE to LISTENING"), strings.Contains(event, "INITIALIZING to LISTENING"):
		return LISTENING, nil
	default:
		return UNKNOWN, fmt.Errorf("unrecognized role in event: %s", event)
	}
}
