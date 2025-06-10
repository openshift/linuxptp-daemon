package parser

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	sstate "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/state"
)

const (
	GM = "gm"
)

func NewGMExtractor(state *sstate.SharedState) *BaseMetricsExtractor {
	return &BaseMetricsExtractor{
		ProcessNameStr: constants.GM,
		ExtractSummaryFn: func(logLine string) (*Metrics, error) {
			return extractSummaryGM(logLine)
		},
		ExtractRegularFn: func(logLine string) (*Metrics, error) {
			return extractRegularGM(logLine)
		},
		ExtraEventFn: func(output string) (*PTPEvent, error) {
			return extractEventGM(output)
		},
		State: state,
	}
}

func extractEventGM(output string) (*PTPEvent, error) {
	// TODO: Implement GM event extraction
	return nil, nil
}

func extractSummaryGM(output string) (*Metrics, error) {
	// TODO: Implement GM summary extraction
	return nil, nil
}

func extractRegularGM(output string) (*Metrics, error) {
	// TODO: Implement GM regular extraction
	return nil, nil
}
