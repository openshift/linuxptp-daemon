package parser

import (
	sstate "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/state"
)

func NewDPLLExtractor(state *sstate.SharedState) *BaseMetricsExtractor {
	return &BaseMetricsExtractor{
		ProcessNameStr: DPLL,
		ExtractSummaryFn: func(logLine string) (*Metrics, error) {
			return extractSummaryDPLL(logLine)
		},
		ExtractRegularFn: func(logLine string) (*Metrics, error) {
			return extractRegularDPLL(logLine)
		},
		ExtraEventFn: func(logLine string) (*PTPEvent, error) {
			return extractEventDPLL(logLine)
		},
		State: state,
	}
}

func extractEventDPLL(logLine string) (*PTPEvent, error) {
	// TODO: Implement DPLL event extraction
	return nil, nil
}

func extractSummaryDPLL(logLine string) (*Metrics, error) {
	// TODO: Implement DPLL summary extraction
	return nil, nil
}

func extractRegularDPLL(logLine string) (*Metrics, error) {
	// TODO: Implement DPLL regular extraction
	return nil, nil
}
