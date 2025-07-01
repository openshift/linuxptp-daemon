package parser

import (
	"errors"
	"regexp"
	"strconv"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

// MetricsExtractor is an interface for extracting metrics from log lines.
var (
	// phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms 4 max 4 freq -76829 +/- 0 delay 1085 +/- 0
	summaryPhc2SysRegex = regexp.MustCompile(
		`^phc2sys\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.config):?(?P<serverity>\d*)\]` +
			`\s+(?P<clock_name>CLOCK_REALTIME)` +
			`\s+rms\s+(?P<offset>-?\d+)` +
			`\s+max\s+(?P<max>-?\d+)` +
			`\s+freq\s+(?P<freq_adj>[-+]?\d+)\s+(?:\+/-\s+\d+)` +
			`\s*(?:delay\s+(?P<delay>\d+)\s+\+/-\s+\d+)?$`,
	)
	regularPhc2SysRegex = regexp.MustCompile(
		`^phc2sys\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.config):?(?P<serverity>\d*)\]` +
			`\s+(?P<clock_name>CLOCK_REALTIME)` +
			`\s+(?P<source>phc|sys)` +
			`\s+offset\s+(?P<offset>-?\d+)` +
			`\s+(?P<servo_state>s\d)` +
			`\s+freq\s+(?P<freq_adj>[-+]?\d+)` +
			`\s*(?:delay\s+(?P<delay>[-+]?\d+))?$`,
	)
)

type phc2sysParsed struct {
	Raw            string
	Timestamp      string
	ConfigName     string
	ServerityLevel *int
	ClockName      string
	Offset         *float64
	MaxOffset      *float64
	FreqAdj        *float64
	Delay          *float64
	ServoState     string
	Source         string
}

// Populate ...
func (p *phc2sysParsed) Populate(line string, matched, feilds []string) error {
	p.Raw = line
	for i, field := range feilds {
		switch field {
		case "timestamp":
			p.Timestamp = matched[i]
		case "config_name":
			p.ConfigName = matched[i]
		case "serverity":
			if matched[i] == "" { // serverity is optional
				continue
			}
			serverityLevel, err := strconv.Atoi(matched[i])
			if err != nil {
				return err
			}
			p.ServerityLevel = &serverityLevel
		case "clock_name":
			p.ClockName = matched[i]
		case "offset":
			if matched[i] == "" {
				return errors.New("offset cannot be empty")
			}
			offset, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.Offset = &offset
		case "max":
			if matched[i] == "" {
				return errors.New("max cannot be empty")
			}
			maxOffset, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.MaxOffset = &maxOffset
		case "freq_adj":
			if matched[i] == "" {
				return errors.New("freq_adj cannot be empty")
			}
			freqAdj, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.FreqAdj = &freqAdj
		case "delay":
			if matched[i] == "" { // Delay is optional
				continue
			}
			delay, err := strconv.ParseFloat(matched[i], 64)
			if err != nil {
				return err
			}
			p.Delay = &delay
		case "servo_state":
			p.ServoState = matched[i]
		case "source":
			p.Source = matched[i]
		}
	}
	return nil
}

// NewPhc2SysExtractor creates a new metrics extractor for phc2sys process
func NewPhc2SysExtractor() *BaseMetricsExtractor[*phc2sysParsed] {
	return &BaseMetricsExtractor[*phc2sysParsed]{
		ProcessNameStr: constants.PHC2SYS,
		NewParsed:      func() *phc2sysParsed { return &phc2sysParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*phc2sysParsed]{
			{
				Regex: summaryPhc2SysRegex,
				Extractor: func(parsed *phc2sysParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractSummaryPhc2Sys(parsed)
					return metric, nil, err
				},
			},
			{
				Regex: regularPhc2SysRegex,
				Extractor: func(parsed *phc2sysParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractRegularPhc2Sys(parsed)
					return metric, nil, err
				},
			},
		},
	}
}

// extractSummaryPhc2Sys parses summary metrics from phc2sys output
// Expected format: phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms 4 max 4 freq -76829 +/- 0 delay 1085 +/- 0
func extractSummaryPhc2Sys(parsed *phc2sysParsed) (*Metrics, error) {
	if parsed.Offset == nil {
		return nil, errors.New("failed to find offset")
	}

	if parsed.MaxOffset == nil {
		return nil, errors.New("failed to find max offset")
	}

	if parsed.FreqAdj == nil {
		return nil, errors.New("failed to find freq adj")
	}

	var delay float64
	if parsed.Delay == nil {
		glog.Warning("delay is missing")
	} else {
		delay = *parsed.Delay
	}

	return &Metrics{
		Iface:     constants.ClockRealTime,
		Offset:    *parsed.Offset,
		MaxOffset: *parsed.MaxOffset,
		FreqAdj:   *parsed.FreqAdj,
		Delay:     delay,
		Source:    constants.Master,
	}, nil
}

// extractRegularPhc2Sys parses regular metrics from phc2sys output
// Expected format: phc2sys[10522413.392]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset 8 s2 freq -6990 delay 502
func extractRegularPhc2Sys(parsed *phc2sysParsed) (*Metrics, error) {
	if parsed.Offset == nil {
		return nil, errors.New("failed to find offset")
	}
	if parsed.FreqAdj == nil {
		return nil, errors.New("failed to find freq adj")
	}

	var delay float64
	if parsed.Delay == nil {
		glog.Warning("delay is missing")
	} else {
		delay = *parsed.Delay
	}

	if parsed.Source == "" {
		return nil, errors.New("failed to find source")
	}
	clockState := clockStateFromServo(parsed.ServoState)

	return &Metrics{
		Iface:      constants.ClockRealTime,
		Offset:     *parsed.Offset,
		MaxOffset:  *parsed.Offset,
		FreqAdj:    *parsed.FreqAdj,
		Delay:      delay,
		ClockState: clockState,
		Source:     parsed.Source,
	}, nil
}
