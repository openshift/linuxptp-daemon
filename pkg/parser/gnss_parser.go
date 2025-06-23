package parser

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
)

type gnssParsed struct {
	Raw       string
	Interface string
}

// Populate ...
func (p *gnssParsed) Populate(line string, matched, fields []string) error {
	p.Raw = line
	for i, field := range fields {
		switch field {
		case constants.Interface:
			p.Interface = matched[i]
		}
	}
	return nil
}

// NewGNSSExtractor NewGNSSTExtractor creates a new metrics extractor for GNSS process
func NewGNSSExtractor() *BaseMetricsExtractor[*gnssParsed] {
	return &BaseMetricsExtractor[*gnssParsed]{
		ProcessNameStr:      constants.GNSS,
		NewParsed:           func() *gnssParsed { return &gnssParsed{} },
		RegexExtractorPairs: []RegexExtractorPair[*gnssParsed]{},
	}
}
