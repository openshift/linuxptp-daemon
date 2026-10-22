package parser

import (
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

var (
	// ts2phc[82674.465]: [ts2phc.0.cfg] ens2f1 master offset          0 s2 freq      -0
	// ts2phc[521734.693]: [ts2phc.0.config:6] /dev/ptp6 offset          0 s2 freq      -0
	regularTS2PhcRegex = regexp.MustCompile(
		`^ts2phc\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.c.*g):?(?P<severity>\d*)\]` +
			`\s+(?P<interface>[\w|\/]+)` +
			`\s*(?P<clock_name>master)?` +
			`\s+offset\s+(?P<offset>-?\d+)` +
			`\s+(?P<servo_state>s\d+)?` +
			`\s+freq\s+(?P<freq>[-+]?\d+)` +
			`(?:\s*(?P<holdover>holdover))?$`,
	)

	// ts2phc[1726600506]:[ts2phc.0.config] ens7f0 nmea_status 1 offset 0 s2
	// ts2phc[1699929121]:[ts2phc.0.config] ens2f0 nmea_status 0 offset 999999 s0
	nmeaStatusTS2PhcRegex = regexp.MustCompile(
		`^ts2phc\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s*\[(?P<config_name>.*\.\d+\.c.*g):?(?P<severity>\d*)\]` +
			`\s+(?P<interface>[\w|\/]+)` +
			`\s+nmea_status\s+(?P<status>[0|1])` +
			`\s+offset\s+(?P<offset>-?\d+)` +
			`\s+(?P<servo_state>s\d+)?`,
	)

	// ts2phc[20748687.391]: [ts2phc.0.config:3] source ts not valid
	noSourceTSRegex = regexp.MustCompile(
		`^ts2phc\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s*\[(?P<config_name>.*\.\d+\.c.*g):?(?P<severity>\d*)\]` +
			`\s*source ts not valid$`,
	)
	// NoSourceTSCount is reset to 0 with each valid timestamp(TS).
	// Only will reach 2 with multiple logs in a row without valid TS.
	// If it reaches 2, then ts2phc should leave holdover.
	// This is required because ts2phc doesn't emit log specifically for
	// exiting holdover.
	// Not concerned about concurrency; will get to 2 even with race condition
	NoSourceTSCount = 0

	// ts2phc[9979.140]: [ts2phc.1.config:7] adding tstamp 1791226372.999999988 to clock /dev/ptp0
	// ts2phc[9979.140]: [ts2phc.1.config:7] adding tstamp 1791226372.787084270 to clock /dev/ptp1
	// ts2phc[9979.626]: [ts2phc.1.config:7] adding tstamp 1791226372.999999870 to clock /dev/ptp2
	tstampEventTS2PHCRegex = regexp.MustCompile(
		`^ts2phc\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.c.*g):?(?P<severity>\d*)\]` +
			`\s+adding tstamp\s+(?P<tstamp>\d+\.\d+)` +
			`\s+to clock\s+(?P<phc>\S+)$`,
	)

	// ts2phc[1031603.223]: [ts2phc.1.config:6] unselecting /dev/ptp9 for synchronization
	selectionTS2PHCRegex = regexp.MustCompile(
		`^ts2phc\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s*\[(?P<config_name>.*\.\d+\.c.*g):?(?P<severity>\d*)\]` +
			`\s+(?P<selection>selecting|unselecting)\s+(?P<phc>/dev/ptp\d+)\s+for synchronization$`,
	)

	// Without external-PPS mode, choosing a reference also clears is_target.
	// ts2phc[9979.140]: [ts2phc.1.config:6] selecting /dev/ptp9 as the reference clock
	referenceTS2PHCRegex = regexp.MustCompile(
		`^ts2phc\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s*\[(?P<config_name>.*\.\d+\.c.*g):?(?P<severity>\d*)\]` +
			`\s+selecting\s+(?P<phc>/dev/ptp\d+)\s+as the reference clock$`,
	)

	// ts2phc[9979.140]: [ts2phc.1.config:6] no reference clock, selecting /dev/ptp9 by default
	defaultReferenceTS2PHCRegex = regexp.MustCompile(
		`^ts2phc\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s*\[(?P<config_name>.*\.\d+\.c.*g):?(?P<severity>\d*)\]` +
			`\s+no reference clock, selecting\s+(?P<phc>/dev/ptp\d+)\s+by default$`,
	)

	// ts2phc[9979.473]: [ts2phc.1.config:7] poll returns zero, no events
	tstampTimeoutTS2PHCRegex = regexp.MustCompile(
		`^ts2phc\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.c.*g):?(?P<severity>\d*)\]` +
			`\s+(?P<pollTimeout>poll returns zero, no events)$`,
	)
)

type ts2phcParsed struct {
	Raw           string
	Timestamp     string
	ConfigName    string
	SeverityLevel *int
	Interface     string
	ClockName     string
	Status        *float64
	Offset        *float64
	ServoState    string
	FreqAdj       *float64
	Holdover      string
	PHCTstamp     *PHCTimestamp
	PHCDevice     string
	PollTimeout   bool
	Role          TS2PHCRole
}

func parsePHCTimestamp(value string) (PHCTimestamp, error) {
	secondsText, fraction, hasFraction := strings.Cut(value, ".")
	seconds, err := strconv.ParseInt(secondsText, 10, 64)
	if err != nil {
		return PHCTimestamp{}, err
	}
	if !hasFraction {
		return PHCTimestamp{Seconds: seconds}, nil
	}
	if fraction == "" || len(fraction) > 9 {
		return PHCTimestamp{}, errors.New("PHC timestamp fraction must contain 1 to 9 digits")
	}
	nanoseconds, err := strconv.ParseInt(fraction+strings.Repeat("0", 9-len(fraction)), 10, 64)
	if err != nil {
		return PHCTimestamp{}, err
	}
	return PHCTimestamp{Seconds: seconds, Nanoseconds: nanoseconds}, nil
}

func (p *ts2phcParsed) Populate(line string, matched, fields []string) error {
	p.Raw = line
	for i, field := range fields {
		switch field {
		case constants.Timestamp:
			p.Timestamp = matched[i]
		case constants.ConfigName:
			p.ConfigName = matched[i]
		case constants.Severity:
			if matched[i] == "" { // severity is optional
				continue
			}
			severityLevel, err := strconv.Atoi(matched[i])
			if err != nil {
				return err
			}
			p.SeverityLevel = &severityLevel
		case constants.Interface:
			p.Interface = matched[i]
		case "clock_name":
			p.ClockName = matched[i]
		case "status":
			if matched[i] != "" {
				value, err := strconv.ParseFloat(matched[i], 64)
				if err != nil {
					return err
				}
				p.Status = &value
			}
		case constants.Offset:
			offset, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.Offset = &offset
		case constants.ServoState:
			p.ServoState = matched[i]
		case "freq":
			freqAdj, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.FreqAdj = &freqAdj
		case "holdover":
			p.Holdover = matched[i]
		case "tstamp":
			tstamp, err := parsePHCTimestamp(matched[i])
			if err != nil {
				return err
			}
			p.PHCTstamp = &tstamp
		case "phc":
			p.PHCDevice = matched[i]
		case "pollTimeout":
			p.PollTimeout = true
		case "selection":
			p.Role = TS2PHCUnmanaged
			if matched[i] == "selecting" {
				p.Role = TS2PHCSink
			}
		}
	}
	return nil
}

// NewTS2PHCExtractor creates a new TS2PHC metrics extractor
func NewTS2PHCExtractor() *BaseMetricsExtractor[*ts2phcParsed] {
	return &BaseMetricsExtractor[*ts2phcParsed]{
		ProcessNameStr: constants.TS2PHC,
		NewParsed: func() *ts2phcParsed {
			return &ts2phcParsed{}
		},
		RegexExtractorPairs: []RegexExtractorPair[*ts2phcParsed]{
			{
				Regex:     regularTS2PhcRegex,
				Extractor: extractTS2PHCOffset,
			},
			{
				Regex:     nmeaStatusTS2PhcRegex,
				Extractor: extractTS2PHCnmeaStatus,
			},
			{
				Regex: noSourceTSRegex,
				Extractor: func(parsed *ts2phcParsed) (*Metrics, *PTPEvent, error) {
					extractNoSourceTS(parsed)
					return nil, nil, nil
				},
			},
			{
				Regex:     tstampEventTS2PHCRegex,
				Extractor: extractTS2PHCPollEvent,
			},
			{
				Regex:     tstampTimeoutTS2PHCRegex,
				Extractor: extractTS2PHCPollEvent,
			},
			{
				Regex:     selectionTS2PHCRegex,
				Extractor: extractTS2PHCPollEvent,
			},
			{
				Regex:     referenceTS2PHCRegex,
				Extractor: extractTS2PHCReferenceEvent,
			},
			{
				Regex:     defaultReferenceTS2PHCRegex,
				Extractor: extractTS2PHCReferenceEvent,
			},
		},
	}
}

func extractTS2PHCOffset(parsed *ts2phcParsed) (*Metrics, *PTPEvent, error) {
	if parsed.Interface == "" {
		return nil, nil, errors.New("ts2phc interface is empty")
	}

	if parsed.Offset == nil {
		return nil, nil, errors.New("ts2phc offset is empty")
	}

	if parsed.FreqAdj == nil {
		return nil, nil, errors.New("ts2phc freq is empty")
	}

	clockState := clockStateFromServo(parsed.ServoState)
	if parsed.Holdover != "" {
		clockState = constants.ClockStateHoldover
	}

	NoSourceTSCount = 0

	metrics := &Metrics{
		From:       constants.TS2PHC,
		Iface:      parsed.Interface,
		Offset:     *parsed.Offset,
		MaxOffset:  *parsed.Offset,
		ClockState: clockState,
		FreqAdj:    *parsed.FreqAdj,
		Source:     constants.Master,
	}
	event := &PTPEvent{
		Iface: parsed.Interface,
		Raw:   parsed.Raw,
		Ts2Phc: &Ts2PhcEvent{
			Timestamp:      parsed.Timestamp,
			PollSuccessful: true,
		},
	}
	return metrics, event, nil
}

func extractTS2PHCnmeaStatus(parsed *ts2phcParsed) (*Metrics, *PTPEvent, error) {
	if parsed.Interface == "" {
		return nil, nil, errors.New("ts2phc interface is empty")
	}

	if parsed.Offset == nil {
		return nil, nil, errors.New("ts2phc offset is empty")
	}

	clockState := clockStateFromServo(parsed.ServoState)

	// Create status metrics list
	var statusMetrics []StatusMetric
	if parsed.Status != nil {
		statusMetrics = append(statusMetrics, StatusMetric{
			Subtype: string(constants.NmeaStatus),
			Status:  *parsed.Status,
		})
	}

	return &Metrics{
		From:       constants.TS2PHC,
		Iface:      parsed.Interface,
		Offset:     *parsed.Offset,
		MaxOffset:  *parsed.Offset,
		ClockState: clockState,
		Source:     constants.NmeaStatus,
		Status:     statusMetrics,
	}, nil, nil
}

func extractNoSourceTS(*ts2phcParsed) {
	if NoSourceTSCount < 2 {
		NoSourceTSCount++
	}
}

// Reference-selection messages identify a source, not just an unmanaged PHC.
func extractTS2PHCReferenceEvent(parsed *ts2phcParsed) (*Metrics, *PTPEvent, error) {
	parsed.Role = TS2PHCSource
	return extractTS2PHCPollEvent(parsed)
}

func extractTS2PHCPollEvent(parsed *ts2phcParsed) (*Metrics, *PTPEvent, error) {
	return nil, &PTPEvent{
		Raw: parsed.Raw,
		Ts2Phc: &Ts2PhcEvent{
			Timestamp:   parsed.Timestamp,
			PHCDevice:   parsed.PHCDevice,
			PHCTstamp:   parsed.PHCTstamp,
			PollTimeout: parsed.PollTimeout,
			Role:        parsed.Role,
		},
	}, nil
}
