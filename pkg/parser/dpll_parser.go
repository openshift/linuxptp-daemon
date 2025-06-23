package parser

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"

type dpllParsed struct {
	Raw       string
	Interface string
}

// Populate ...
func (p *dpllParsed) Populate(line string, matched, fields []string) error {
	p.Raw = line
	for i, field := range fields {
		switch field {
		case constants.Interface:
			p.Interface = matched[i]
		}
	}
	return nil
}

// NewDPLLExtractor ...
func NewDPLLExtractor() *BaseMetricsExtractor[*dpllParsed] {
	return &BaseMetricsExtractor[*dpllParsed]{
		ProcessNameStr:      constants.DPLL,
		NewParsed:           func() *dpllParsed { return &dpllParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*dpllParsed]{},
	}
}
