package parser

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

type gmParsed struct {
	Raw       string
	Interface string
}

// Populate ...
func (p *gmParsed) Populate(line string, matched, fields []string) error {
	p.Raw = line
	for i, field := range fields {
		switch field {
		case constants.Interface:
			p.Interface = matched[i]
		}
	}
	return nil
}

// NewGMExtractor ...
func NewGMExtractor() *BaseMetricsExtractor[*gmParsed] {
	return &BaseMetricsExtractor[*gmParsed]{
		ProcessNameStr:      event.GmProcessName,
		NewParsed:           func() *gmParsed { return &gmParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*gmParsed]{},
	}
}
