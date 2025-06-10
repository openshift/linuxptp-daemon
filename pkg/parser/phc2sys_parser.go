package parser

import (
	"fmt"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	sstate "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/state"
	"regexp"
)

// MetricsExtractor is an interface for extracting metrics from log lines.
var (
	// phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms 4 max 4 freq -76829 +/- 0 delay 1085 +/- 0
	summaryPhc2SysRegex = regexp.MustCompile(`^phc2sys\[\d+\.\d+\]:\s+\[.*\.\d+\.config:?\d*\]\s*(?P<interface>\w+)?\s+(?P<clock>\w+)\s+rms\s+(?P<rms>\d+)\s+max\s+(?P<max>-?\d+)\s+freq\s+(?P<freq_adj>[-+]\d+)\s+\+/-\s+\d+\s*(?:delay\s+(?P<delay>\d+)\s+\+/-\s+\d+)?$`)
	regularPhc2SysRegex = regexp.MustCompile(`^phc2sys\[\d+\.\d+\]:\s+\[.*\.\d+\.config:?\d*\]\s*(?P<interface>\w+)?\s+(?P<clock>\w+)\s+(?P<offset_type>phc|sys)\s+offset\s+(?P<offset>\d+)\s(?P<status>s\d)\s+freq\s+(?P<freq_adj>[-+]\d+)\s*(?:delay\s+(?P<delay>\d+))?$`)
)

// NewPhc2SysExtractor creates a new metrics extractor for phc2sys process
func NewPhc2SysExtractor(state *sstate.SharedState) *BaseMetricsExtractor {
	return &BaseMetricsExtractor{
		ProcessNameStr: constants.PHC2SYS,
		ExtractSummaryFn: func(logLine string) (*Metrics, error) {
			return extractSummaryPhc2Sys(logLine)
		},
		ExtractRegularFn: func(logLine string) (*Metrics, error) {
			return extractRegularPhc2Sys(logLine)
		},
		ExtraEventFn: nil,
		State:        state,
	}
}

// extractSummaryPhc2Sys parses summary metrics from phc2sys output
// Expected format: phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms 4 max 4 freq -76829 +/- 0 delay 1085 +/- 0
func extractSummaryPhc2Sys(output string) (*Metrics, error) {
	if output == "" {
		return nil, fmt.Errorf("empty output")
	}

	match := summaryPhc2SysRegex.FindStringSubmatch(output)
	if match == nil {
		return nil, fmt.Errorf("no match for summary regex: %s", output)
	}

	groups := summaryPhc2SysRegex.SubexpNames()
	result := map[string]string{}
	for i, name := range groups {
		if i > 0 && name != "" {
			result[name] = match[i]
		}
	}

	if result["clock"] != clockRealTime {
		return nil, fmt.Errorf("non-realtime clock: %s", result["clock"])
	}

	rmsOffset, err := validateFloat(result["rms"], "rms offset")
	if err != nil {
		return nil, err
	}

	maxOffset, err := validateFloat(result["max"], "max offset")
	if err != nil {
		return nil, err
	}

	freqAdj, err := validateFloat(result["freq_adj"], "frequency adjustment")
	if err != nil {
		return nil, err
	}

	delay, err := validateFloat(result["delay"], "delay")
	if err != nil {
		// Delay is optional, set to 0 if not present
		delay = 0
	}

	return &Metrics{
		Iface:     result["clock"],
		Offset:    rmsOffset,
		MaxOffset: maxOffset,
		FreqAdj:   freqAdj,
		Delay:     delay,
		Source:    master,
	}, nil
}

// extractRegularPhc2Sys parses regular metrics from phc2sys output
// Expected format: phc2sys[10522413.392]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset 8 s2 freq -6990 delay 502
func extractRegularPhc2Sys(output string) (*Metrics, error) {
	if output == "" {
		return nil, fmt.Errorf("empty output")
	}

	match := regularPhc2SysRegex.FindStringSubmatch(output)
	if match == nil {
		return nil, fmt.Errorf("no match for regular regex: %s", output)
	}

	groups := regularPhc2SysRegex.SubexpNames()
	result := map[string]string{}
	for i, name := range groups {
		if i > 0 && name != "" {
			result[name] = match[i]
		}
	}

	if result["clock"] != clockRealTime {
		return nil, fmt.Errorf("ignore non CLOCK_REALTIME: %s", output)
	}

	ptpOffset, err := validateFloat(result["offset"], "offset")
	if err != nil {
		return nil, err
	}

	freqAdj, err := validateFloat(result["freq_adj"], "frequency adjustment")
	if err != nil {
		return nil, err
	}

	delay, err := validateFloat(result["delay"], "delay")
	if err != nil {
		// Delay is optional, set to 0 if not present
		delay = 0
	}

	clockState := parseClockState(result["status"])
	source := result["offset_type"] // phc or sys

	return &Metrics{
		Iface:      result["clock"],
		Offset:     ptpOffset,
		MaxOffset:  ptpOffset,
		FreqAdj:    freqAdj,
		Delay:      delay,
		ClockState: clockState,
		Source:     source,
	}, nil
}
