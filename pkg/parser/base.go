package parser

import (
	"regexp"
)

// MetricsExtractor is an interface for extracting metrics from log lines.
type MetricsExtractor interface {
	ProcessName() string
	Extract(logLine string) (*Metrics, error)
}

// Populatable ...
type Populatable interface {
	Populate(line string, matched, fields []string) error
}

// RegexExtractorPair ...
type RegexExtractorPair[P Populatable] struct {
	Regex     *regexp.Regexp
	Extractor func(P) (*Metrics, *PTPEvent, error)
}

// BaseMetricsExtractor is a base struct for all metrics extractors.
// It provides common functionality for extracting metrics from log lines.
type BaseMetricsExtractor[P Populatable] struct {
	ProcessNameStr      string // Process name (e.g., "ptp4l", "phc2sys")
	NewParsed           func() P
	RegexExtractorPairs []RegexExtractorPair[P]
}

// ProcessName returns the name of the process that is being extracted.
func (b *BaseMetricsExtractor[P]) ProcessName() string {
	return b.ProcessNameStr
}

// Extract extracts metrics from a log line.
// It determines the type of log line and calls the appropriate extraction function.
func (b *BaseMetricsExtractor[P]) Extract(logLine string) (*Metrics, *PTPEvent, error) {
	if logLine == "" {
		return nil, nil, nil
	}
	for _, pair := range b.RegexExtractorPairs {
		match := pair.Regex.FindStringSubmatch(logLine)
		if match == nil { // No match move on to next one
			continue
		}
		groups := pair.Regex.SubexpNames()
		result := b.NewParsed()
		err := result.Populate(logLine, match, groups)
		if err != nil {
			return nil, nil, err
		}
		return pair.Extractor(result)
	}
	return nil, nil, nil
}
