package parser

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	_ "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/metrics"
	sstate "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/state"
)

func NewSynceExtractor(state *sstate.SharedState) *BaseMetricsExtractor {
	return &BaseMetricsExtractor{
		ProcessNameStr: constants.SYNCE,
		ExtractSummaryFn: func(logLine string) (*Metrics, error) {
			return extractSummarySynce(logLine)
		},
		ExtractRegularFn: func(logLine string) (*Metrics, error) {
			return extractRegularSynce(logLine)
		},
		ExtraEventFn: func(output string) (*PTPEvent, error) {
			return extractEventSynce(output)
		},
		State: state,
	}
}

func extractEventSynce(output string) (*PTPEvent, error) {
	// TODO: Implement event extraction for synce
	return nil, nil
}

func extractSummarySynce(output string) (*Metrics, error) {
	// TODO: Implement summary extraction for synce
	return nil, nil
}

func extractRegularSynce(output string) (*Metrics, error) {
	// TODO: Implement regular extraction for synce
	return nil, nil
}
