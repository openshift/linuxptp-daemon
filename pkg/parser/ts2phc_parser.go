package parser

import (
	"errors"
	"regexp"
	"strconv"

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
				Regex: regularTS2PhcRegex,
				Extractor: func(parsed *ts2phcParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractTS2PHCOffset(parsed)
					return metric, nil, err
				},
			},
			{
				Regex: nmeaStatusTS2PhcRegex,
				Extractor: func(parsed *ts2phcParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractTS2PHCnmeaStatus(parsed)
					return metric, nil, err
				},
			},
		},
	}
}

func extractTS2PHCOffset(parsed *ts2phcParsed) (*Metrics, error) {
	if parsed.Interface == "" {
		return nil, errors.New("ts2phc interface is empty")
	}

	if parsed.Offset == nil {
		return nil, errors.New("ts2phc offset is empty")
	}

	if parsed.FreqAdj == nil {
		return nil, errors.New("ts2phc freq is empty")
	}

	clockState := clockStateFromServo(parsed.ServoState)
	if parsed.Holdover != "" {
		clockState = constants.ClockStateHoldover
	}

	return &Metrics{
		From:       constants.TS2PHC,
		Iface:      parsed.Interface,
		Offset:     *parsed.Offset,
		MaxOffset:  *parsed.Offset,
		ClockState: clockState,
		FreqAdj:    *parsed.FreqAdj,
		Source:     constants.Master,
	}, nil
}

func extractTS2PHCnmeaStatus(parsed *ts2phcParsed) (*Metrics, error) {
	if parsed.Interface == "" {
		return nil, errors.New("ts2phc interface is empty")
	}

	if parsed.Offset == nil {
		return nil, errors.New("ts2phc offset is empty")
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
	}, nil
}
