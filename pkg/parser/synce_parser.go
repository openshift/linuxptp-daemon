package parser

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

type syncEParsed struct {
	Raw       string
	Interface string
}

func (p *syncEParsed) Populate(line string, matched, fields []string) error {
	p.Raw = line
	for i, field := range fields {
		switch field {
		case constants.Interface:
			p.Interface = matched[i]
		}
	}
	return nil
}

// NewSynceExtractor ...
func NewSynceExtractor() *BaseMetricsExtractor[*syncEParsed] {
	return &BaseMetricsExtractor[*syncEParsed]{
		ProcessNameStr:      constants.SYNCE,
		NewParsed:           func() *syncEParsed { return &syncEParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*syncEParsed]{},
	}
}
