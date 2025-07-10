package parser

import (
	"regexp"
	"strings"
)

// MetricsExtractor is an interface for extracting metrics from log lines.
type MetricsExtractor interface {
	ProcessName() string
	Extract(logLine string) (*Metrics, *PTPEvent, error)
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
	logLine = strings.TrimSpace(logLine)
	if logLine == "" {
		return nil, nil, nil
	}
	for _, pair := range b.RegexExtractorPairs {
		parseResult, lineParsed, err := parseLine(logLine, pair.Regex, b.NewParsed)
		if err != nil {
			return nil, nil, err
		}
		if !lineParsed {
			continue
		}
		return pair.Extractor(parseResult)
	}
	return nil, nil, nil
}

func parseLine[P Populatable](logLine string, regex *regexp.Regexp, newParseResult func() P) (P, bool, error) {
	match := regex.FindStringSubmatch(logLine)

	if match == nil { // No match move on to next one
		return newParseResult(), false, nil
	}
	groups := regex.SubexpNames()

	result := newParseResult()
	err := result.Populate(logLine, match, groups)
	if err != nil {
		return newParseResult(), false, err
	}
	return result, true, nil
}
