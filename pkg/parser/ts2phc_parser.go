package parser

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

type ts2phcParsed struct {
	Raw       string
	Interface string
}

func (p *ts2phcParsed) Populate(line string, matched, fields []string) error {
	p.Raw = line
	for i, field := range fields {
		switch field {
		case constants.Interface:
			p.Interface = matched[i]
		}
	}
	return nil
}

// NewTS2PHCExtractor ...
func NewTS2PHCExtractor() *BaseMetricsExtractor[*ts2phcParsed] {
	return &BaseMetricsExtractor[*ts2phcParsed]{
		ProcessNameStr:      constants.TS2PHC,
		NewParsed:           func() *ts2phcParsed { return &ts2phcParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*ts2phcParsed]{},
	}
}
