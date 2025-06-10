package parser

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"

	sstate "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/state"
)

const (
	TS2PHC = "ts2phc"
)

func NewTS2PHCExtractor(state *sstate.SharedState) *BaseMetricsExtractor {
	return &BaseMetricsExtractor{
		ProcessNameStr: constants.TS2PHC,
		ExtractSummaryFn: func(logLine string) (*Metrics, error) {
			return extractSummaryTS2PHC(logLine)
		},
		ExtractRegularFn: func(logLine string) (*Metrics, error) {
			return extractRegularTS2PHC(logLine)
		},
		ExtraEventFn: func(output string) (*PTPEvent, error) {
			return extractEventTS2PHC(output)
		},
		State: state,
	}
}

func extractEventTS2PHC(output string) (*PTPEvent, error) {
	// TODO: Implement event extraction for ts2phc
	return nil, nil
}

func extractSummaryTS2PHC(output string) (*Metrics, error) {
	// TODO: Implement summary extraction for ts2phc
	return nil, nil
}

func extractRegularTS2PHC(output string) (*Metrics, error) {
	// TODO: Implement regular extraction for ts2phc
	return nil, nil
}
