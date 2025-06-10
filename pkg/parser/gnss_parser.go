package parser

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	sstate "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/state"
)

// NewGNSSExtractor NewGNSSTExtractor creates a new metrics extractor for GNSS process
func NewGNSSExtractor(state *sstate.SharedState) *BaseMetricsExtractor {
	return &BaseMetricsExtractor{
		ProcessNameStr: constants.GNSS,
		ExtractSummaryFn: func(logLine string) (*Metrics, error) {
			return extractSummaryGNSS(logLine)
		},
		ExtractRegularFn: func(logLine string) (*Metrics, error) {
			return extractRegularGNSS(logLine)
		},
		ExtraEventFn: nil,
		State:        state,
	}
}

// extractSummaryGNSS parses summary metrics from GNSS output
func extractSummaryGNSS(output string) (*Metrics, error) {
	// TODO: Implement GNSS summary metrics extraction
	return nil, nil
}

// extractRegularGNSS parses regular metrics from GNSS output
func extractRegularGNSS(output string) (*Metrics, error) {
	// TODO: Implement GNSS regular metrics extraction
	return nil, nil
}
