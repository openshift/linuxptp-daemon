package parser

import (
	"regexp"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

var (
	// chronyd[1234][chronyd.0.config]: Selected source 192.168.1.1 (1.2.3.4)
	chronydSelectedSourceRegex = regexp.MustCompile(
		`chronyd\[\d+\]\[(?P<config_name>.*\.\d+\.config)\]:\s+Selected source\s+`,
	)
	// chronyd[1234][chronyd.0.config]: Can't synchronise: no selectable sources
	chronydNoSelectableSourcesRegex = regexp.MustCompile(
		`chronyd\[\d+\]\[(?P<config_name>.*\.\d+\.config)\]:\s+.*no selectable sources`,
	)
)

type chronydParsed struct {
	ConfigName string
	Locked     bool
}

func (p *chronydParsed) Populate(_ string, matched, fields []string) error {
	for i, field := range fields {
		switch field {
		case "config_name":
			p.ConfigName = matched[i]
		}
	}
	return nil
}

// NewChronydExtractor creates a new metrics extractor for chronyd process
func NewChronydExtractor() *BaseMetricsExtractor[*chronydParsed] {
	return &BaseMetricsExtractor[*chronydParsed]{
		ProcessNameStr: constants.CHRONYD,
		NewParsed:      func() *chronydParsed { return &chronydParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*chronydParsed]{
			{
				Regex: chronydSelectedSourceRegex,
				Extractor: func(_ *chronydParsed) (*Metrics, *PTPEvent, error) {
					return &Metrics{
						Iface:      constants.ClockRealTime,
						ClockState: constants.ClockStateLocked,
						Offset:     0,
						Source:     constants.CHRONYD,
					}, nil, nil
				},
			},
			{
				Regex: chronydNoSelectableSourcesRegex,
				Extractor: func(_ *chronydParsed) (*Metrics, *PTPEvent, error) {
					return &Metrics{
						Iface:      constants.ClockRealTime,
						ClockState: constants.ClockStateFreeRun,
						Offset:     0,
						Source:     constants.CHRONYD,
					}, nil, nil
				},
			},
		},
	}
}
