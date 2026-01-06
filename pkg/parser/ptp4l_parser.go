package parser

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

var (

	// ptp4l[4268779.809]: [ptp4l.3.config] port 3: UNCALIBRATED to MASTER on RS_MASTER
	// ptp4l[4268779.809]: [ptp4l.4.config] port 4: FAULT_DETECTED
	// ptp4l[412707.219]: [ptp4l.0.config:5] port 11 (ens8f2): LISTENING to MASTER on ANNOUNCE_RECEIPT_TIMEOUT_EXPIRES
	ptp4lEventRegex = regexp.MustCompile(
		`^ptp4l\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.config):?(?P<serverity>\d*)\]` +
			`\s+port\s+(?P<port_id>\d+)(?:\s+\((?P<port_name>[\d\w]+)\))?:` +
			`\s+(?P<event>.+)`,
	)
	// ptp4l[74737.942]: [ptp4l.0.config] rms 53 max 74 freq -16642 +/- 40 delay 1089 +/- 20
	summaryPTP4LRegex = regexp.MustCompile(
		`^ptp4l\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.config):?(?P<serverity>\d*)\]` +
			`\s*(?P<interface>\w+)?` +
			`\s+rms\s+(?P<offset>-?\d+)` +
			`\s+max\s+(?P<max>-?\d+)` +
			`\s+freq\s+(?P<freq_adj>[-+]?\d+)\s+\+/-\s+\d+` +
			`\s*(?:delay\s+(?P<delay>\d+)\s+\+/-\s+\d+)?` +
			`$`,
	)
	// ptp4l[365195.391]: [ptp4l.0.config] master offset -1 s2 freq -3972 path delay 89
	regularPTP4LRegex = regexp.MustCompile(
		`^ptp4l\[(?P<timestamp>\d+\.?\d*)\]:` +
			`\s+\[(?P<config_name>.*\.\d+\.config):?(?P<serverity>\d*)\]` +
			`\s*(?P<interface>\w+)?` +
			`\s+offset\s+(?P<offset>-?\d+)` +
			`\s+(?P<servo_state>s\d)` +
			`\s+freq\s+(?P<freq_adj>[-+]?\d+)` +
			`\s*(?:path\s+delay\s+(?P<delay>\d+))?` +
			`$`,
	)
)

type ptp4lParsed struct {
	// Common
	Raw            string
	Timestamp      string
	ConfigName     string
	ServerityLevel *int

	// Metric
	Interface  string
	Offset     *float64
	MaxOffset  *float64
	FreqAdj    *float64
	Delay      *float64
	ServoState string

	// Event Fields
	PortID   *int
	PortName string
	Event    string
}

// Populate ...
func (p *ptp4lParsed) Populate(line string, matched, feilds []string) error {
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
		case constants.Interface:
			p.Interface = matched[i]
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
		case "port_id":
			portID, err := strconv.Atoi(matched[i])
			if err != nil {
				return err
			}
			p.PortID = &portID
		case "port_name":
			p.PortName = matched[i]
		case "event":
			p.Event = matched[i]
		}
	}
	return nil
}

// NewPTP4LExtractor creates a new PTP4LExtractor.
func NewPTP4LExtractor() *BaseMetricsExtractor[*ptp4lParsed] {
	return &BaseMetricsExtractor[*ptp4lParsed]{
		ProcessNameStr: constants.PTP4L,
		NewParsed:      func() *ptp4lParsed { return &ptp4lParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*ptp4lParsed]{
			{
				Regex: ptp4lEventRegex,
				Extractor: func(parsed *ptp4lParsed) (*Metrics, *PTPEvent, error) {
					event, err := extractEventPTP4l(parsed)
					return nil, event, err
				},
			},
			{
				Regex: summaryPTP4LRegex,
				Extractor: func(parsed *ptp4lParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractSummaryPTP4l(parsed)
					return metric, nil, err
				},
			},
			{
				Regex: regularPTP4LRegex,
				Extractor: func(parsed *ptp4lParsed) (*Metrics, *PTPEvent, error) {
					metric, err := extractRegularPTP4l(parsed)
					return metric, nil, err
				},
			},
		},
	}
}
func extractEventPTP4l(parsed *ptp4lParsed) (*PTPEvent, error) {
	if parsed.PortID == nil {
		return nil, fmt.Errorf("port id not found")
	}
	portID := *parsed.PortID

	role, clockState := determineRole(parsed.Event)
	if role == constants.PortRoleUnknown {
		portID = 0
	}

	// TODO: Pass port name up if provided
	return &PTPEvent{
		PortID:     portID,
		Role:       role,
		ClockState: clockState,
		Raw:        parsed.Raw,
	}, nil
}

// ExtractPortName extracts the port name from a PTP4L event log line
// Returns the port name if found, empty string otherwise
func ExtractPortName(logLine string) string {
	match := ptp4lEventRegex.FindStringSubmatch(logLine)
	if match == nil {
		return "" // Not a PTP4L event log line
	}

	// Find the port_name group index
	groups := ptp4lEventRegex.SubexpNames()
	for i, name := range groups {
		if name == "port_name" && i < len(match) {
			return match[i]
		}
	}

	return "" // Port name not found
}

func extractSummaryPTP4l(parsed *ptp4lParsed) (*Metrics, error) {
	iface := parsed.Interface
	if iface == "" {
		iface = constants.Master
	}
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
		Iface:     iface,
		Offset:    *parsed.Offset,
		MaxOffset: *parsed.MaxOffset,
		FreqAdj:   *parsed.FreqAdj,
		Delay:     delay,
		Source:    constants.Master,
	}, nil
}

func extractRegularPTP4l(parsed *ptp4lParsed) (*Metrics, error) {
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

	if parsed.ServoState == "" {
		return nil, errors.New("failed to find clock state")
	}
	clockState := clockStateFromServo(parsed.ServoState)

	return &Metrics{
		Iface:      parsed.Interface,
		Offset:     *parsed.Offset,
		MaxOffset:  *parsed.Offset,
		FreqAdj:    *parsed.FreqAdj,
		Delay:      delay,
		ClockState: clockState,
		Source:     constants.Master,
	}, nil
}

func determineRole(event string) (constants.PTPPortRole, constants.ClockState) {
	switch {
	case strings.Contains(event, "UNCALIBRATED to SLAVE"),
		strings.Contains(event, "LISTENING to SLAVE"):
		return constants.PortRoleSlave, constants.ClockStateFreeRun
	case strings.Contains(event, "UNCALIBRATED to PASSIVE"),
		strings.Contains(event, "MASTER to PASSIVE"),
		strings.Contains(event, "SLAVE to PASSIVE"),
		strings.Contains(event, "LISTENING to PASSIVE"):
		return constants.PortRolePassive, constants.ClockStateFreeRun
	case strings.Contains(event, "UNCALIBRATED to MASTER"),
		strings.Contains(event, "LISTENING to MASTER"):
		return constants.PortRoleMaster, constants.ClockStateFreeRun
	case strings.Contains(event, "FAULT_DETECTED"),
		strings.Contains(event, "SYNCHRONIZATION_FAULT"),
		strings.Contains(event, "SLAVE to UNCALIBRATED"),
		strings.Contains(event, "MASTER to UNCALIBRATED on RS_SLAVE"),
		strings.Contains(event, "LISTENING to UNCALIBRATED on RS_SLAVE"):
		return constants.PortRoleFaulty, constants.ClockStateHoldover
	case strings.Contains(event, "SLAVE to MASTER"),
		strings.Contains(event, "SLAVE to GRAND_MASTER"):
		return constants.PortRoleMaster, constants.ClockStateHoldover
	case strings.Contains(event, "SLAVE to LISTENING"):
		return constants.PortRoleListening, constants.ClockStateHoldover
	case strings.Contains(event, "FAULTY to LISTENING"),
		strings.Contains(event, "UNCALIBRATED to LISTENING"),
		strings.Contains(event, "INITIALIZING to LISTENING"):
		return constants.PortRoleListening, constants.ClockStateFreeRun
	default:
		return constants.PortRoleUnknown, constants.ClockStateFreeRun
	}
}
