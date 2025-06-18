package logfilter

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"

	"gonum.org/v1/gonum/stat"
)

const (
	defaultEnhancedFilterTime = "30s"
	ptp4lProcessName          = "ptp4l"
	phc2sysProcessName        = "phc2sys"
)

// LogFilter represents a specific filter that can be used to control logging
type LogFilter struct {
	logFilterEnabled        bool
	logFilterRegexStr       string
	logFilterRegex          *regexp.Regexp
	logFilterReducerRegexes []*regexp.Regexp
	batchLength             time.Duration
	expiryTime              time.Time
	counter                 int64
	offsets                 []float64
	summaryText             string
	summaryFields           []string
}

func logFilterFromRegex(regex string, reducers []string, batchLengthStr string, summaryText string, summaryFields []string) *LogFilter {
	var filter LogFilter
	logFilterRegex, logFilterRegexErr := regexp.Compile(regex)
	if logFilterRegexErr != nil {
		glog.Infof("Failed parsing regex %s: %d.  Defaulting to accept all", regex, logFilterRegexErr)
		filter.logFilterEnabled = false
	} else {
		filter.logFilterEnabled = true
		filter.logFilterRegexStr = regex
		filter.logFilterRegex = logFilterRegex
	}

	filter.counter = 0
	batchLength, err := time.ParseDuration(batchLengthStr)
	if err != nil && batchLengthStr != "" {
		glog.Infof("Failed parsing batchLength %s: %d.  Defaulting to 1 Second.", batchLength, err)
		filter.batchLength = time.Second
	} else {
		filter.batchLength = batchLength
	}

	filter.expiryTime = time.Now().Add(filter.batchLength)
	filter.summaryText = summaryText
	filter.summaryFields = summaryFields
	for _, reducer := range reducers {
		reducerRegex, reducerRegexErr := regexp.Compile(reducer)
		if reducerRegexErr != nil {
			glog.Infof("Failed parsing reducer %s: %d.", reducer, reducerRegexErr)
		} else {
			filter.logFilterReducerRegexes = append(filter.logFilterReducerRegexes, reducerRegex)
		}
	}
	return &filter
}

func reprLogFilter(filter *LogFilter) string {
	return filter.logFilterRegexStr
}

func getLogFiltersEnhanced(processName string, messageTag string, batchLength string) []*LogFilter {
	var logFilters []*LogFilter
	offsetString := ""
	if processName == ptp4lProcessName {
		offsetString = "master offset"
	} else if processName == phc2sysProcessName {
		offsetString = "phc offset"
	}

	if offsetString != "" {
		offsetRegex := fmt.Sprintf("^.*%s.*%s.*$", processName, offsetString)
		offsetReducers := []string{offsetString + " *[0-9-]* ", "[-]*[0-9]+"}
		offsetOutputs := []string{"time", "cnt", "min", "max", "avg", "SD"}
		offsetFormatter := processName + "[%0.3f]: " + messageTag + " " + offsetString + " summary: cnt=%d, min=%d, max=%d, avg=%0.2f, SD=%0.2f"
		offsetFilter := logFilterFromRegex(offsetRegex, offsetReducers, batchLength, offsetFormatter, offsetOutputs)
		logFilters = append(logFilters, offsetFilter)
	}

	return logFilters
}

// GetLogFilters gets a list of LogFilters to use based off of specified config settings
func GetLogFilters(processName string, messageTag string, ptpSettings map[string]string) []*LogFilter {
	var logFilters []*LogFilter
	messageTag = strings.ReplaceAll(messageTag, "{level}", "6")

	if filter, ok := ptpSettings["stdoutFilter"]; ok {
		logFilters = append(logFilters, logFilterFromRegex(filter, nil, "", "", nil)) // Filter anything with specified filter
	}
	if logReduce, ok := ptpSettings["logReduce"]; ok {
		if strings.ToLower(logReduce) == "true" || strings.ToLower(logReduce) == "basic" {
			logFilters = append(logFilters, logFilterFromRegex("^.*master offset.*$", nil, "", "", nil)) // Just filter anything with master offset
		} else if strings.ToLower(logReduce) == "enhanced" {
			logFilters = getLogFiltersEnhanced(processName, messageTag, defaultEnhancedFilterTime)
		}
	}

	for index, filter := range logFilters {
		glog.Infof("%s%s logFilterRegex[%d]='%s'\n", processName, messageTag, index, reprLogFilter(filter))
	}

	return logFilters
}

// parseOffsetStats returns a list of provided offsets
// time: The current time since Unix Epoch
// cnt: the number of offsets in the list
// min: the minimum offset from the list
// max: the maximum offset from the list
// avg: the average offset from the list
// med: the median offset from the list
// VAR: the variance of the offsets in the list
// SD: The standard deviation of the offsets in the list
func parseOffsetStats(offsets []float64, fields []string) []any {
	slices.Sort(offsets)
	ret := make([]any, 0)
	var val any
	for _, field := range fields {
		switch field {
		case "time":
			val = float64(time.Now().UnixMilli()) / 1000
		case "cnt":
			val = len(offsets)
		case "min":
			val = int64(offsets[0])
		case "max":
			val = int64(offsets[len(offsets)-1])
		case "avg":
			val = stat.Mean(offsets, nil)
		case "med":
			val = stat.Quantile(0.5, stat.Empirical, offsets, nil)
		case "VAR":
			val = stat.Variance(offsets, nil)
		case "SD":
			val = stat.StdDev(offsets, nil)
		default:
			val = nil
		}
		ret = append(ret, val)
	}

	return ret
}

// FilterOutput filters output based on list of LogFilters
func FilterOutput(logFilters []*LogFilter, output string) string {
	skipOutput := false
	ret := output
	for _, filter := range logFilters {
		if !filter.logFilterEnabled {
			continue
		}
		if filter.logFilterRegex.MatchString(output) {
			filteredOutput := output
			for _, reducer := range filter.logFilterReducerRegexes {
				filteredOutput = reducer.FindString(filteredOutput)
			}
			filteredVal := 0.0
			if filteredOutput != output {
				f, err := strconv.ParseFloat(filteredOutput, 64)
				if err == nil {
					filteredVal = f
				} else {
					glog.Errorf("error parsing filtered value %s", err.Error())
				}
			}
			if filter.summaryText == "" {
				skipOutput = true
			} else if len(filter.offsets) > 0 && time.Now().After(filter.expiryTime) {
				offsetStats := parseOffsetStats(filter.offsets, filter.summaryFields)
				ret = fmt.Sprintf(filter.summaryText, offsetStats...)
				filter.offsets = make([]float64, 0)
				filter.expiryTime = time.Now().Add(filter.batchLength)
			} else {
				filter.offsets = append(filter.offsets, filteredVal)
				skipOutput = true
			}
		}
	}
	if skipOutput {
		ret = ""
	}
	return ret
}
