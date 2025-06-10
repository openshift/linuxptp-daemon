package parser

import (
	"fmt"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/state"
	"strings"
)

// MetricsExtractor is an interface for extracting metrics from log lines.
type MetricsExtractor interface {
	ProcessName() string
	Extract(logLine string) (*Metrics, error)
}

// BaseMetricsExtractor is a base struct for all metrics extractors.
// It provides common functionality for extracting metrics from log lines.
type BaseMetricsExtractor struct {
	ProcessNameStr   string                                 // Process name (e.g., "ptp4l", "phc2sys")
	ExtractSummaryFn func(logLine string) (*Metrics, error) // Function to extract summary metrics
	ExtractRegularFn func(logLine string) (*Metrics, error) // Function to extract regular metrics
	ExtraEventFn     func(logLine string) (*PTPEvent, error) // Function to extract events
	State            *state.SharedState                      // Shared state for tracking PTP configuration
}

// ProcessName returns the name of the process that is being extracted.
func (b *BaseMetricsExtractor) ProcessName() string {
	return b.ProcessNameStr
}

// Extract extracts metrics from a log line.
// It determines the type of log line and calls the appropriate extraction function.
func (b *BaseMetricsExtractor) Extract(output string) (*Metrics, *PTPEvent, error) {
	if output == "" {
		return nil, nil, fmt.Errorf("empty log line")
	}

	output = removeMessageSuffix(output)

	// Determine log line type and extract accordingly
	if strings.Contains(output, " max ") {
		metrics, err := b.ExtractSummaryFn(output)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to extract summary metrics: %v", err)
		}
		return metrics, nil, nil
	} else if strings.Contains(output, " offset ") {
		metrics, err := b.ExtractRegularFn(output)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to extract regular metrics: %v", err)
		}
		return metrics, nil, nil
	} else if b.ExtraEventFn != nil {
		event, err := b.ExtraEventFn(output)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to extract event: %v", err)
		}
		return nil, event, nil
	}

	return nil, nil, fmt.Errorf("log output is not parsable: %s", output)
}

// ExtractEvent extracts an event from a log line.
func (b *BaseMetricsExtractor) ExtractEvent(output string) (*PTPEvent, error) {
	if output == "" {
		return nil, fmt.Errorf("empty log line")
	}

	if b.ExtraEventFn != nil {
		event, err := b.ExtraEventFn(output)
		if err != nil {
			return nil, fmt.Errorf("failed to extract event: %v", err)
		}
		return event, nil
	}
	return nil, fmt.Errorf("event extraction not supported for this extractor")
}
