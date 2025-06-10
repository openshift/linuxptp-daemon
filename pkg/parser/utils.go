package parser

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	PTPNamespace       = "openshift"
	PTPSubsystem       = "ptp"
	GNSS               = "gnss"
	DPLL               = "dpll"
	ptp4lProcessName   = "ptp4l"
	phc2sysProcessName = "phc2sys"
	ts2phcProcessName  = "ts2phc"
	syncEProcessName   = "synce4l"
	clockRealTime      = "CLOCK_REALTIME"
	master             = "master"
	pmcSocketName      = "pmc"

	faultyOffset = 999999

	offset = "offset"
	rms    = "rms"

	// offset source
	phc = "phc"
	sys = "sys"
)

const (
	//LOCKED ...
	LOCKED string = "LOCKED"
	//FREERUN ...
	FREERUN = "FREERUN"
	// HOLDOVER
	HOLDOVER = "HOLDOVER"
)

const (
	PtpProcessDown int64 = 0
	PtpProcessUp   int64 = 1
)

type PTPPortRole int

const (
	PASSIVE PTPPortRole = iota
	SLAVE
	MASTER
	FAULTY
	UNKNOWN
	LISTENING
)

const (
	MessageTagSuffixSeperator = ":"
)

var (
	messageTagSuffixRegEx = regexp.MustCompile(`([a-zA-Z0-9]+\.[a-zA-Z0-9]+\.config):[a-zA-Z0-9]+(:[a-zA-Z0-9]+)?`)
	clockIDRegEx          = regexp.MustCompile(`\/dev\/ptp\d+`)
)

// normalizeLine normalizes log lines by removing brackets and colons
func normalizeLine(line string) string {
	replacer := strings.NewReplacer("[", " ", "]", " ", ":", " ", " phc ", " ", " sys ", " ")
	return replacer.Replace(line)
}

// parseClockState converts PTP state strings to clock state constants
func parseClockState(s string) string {
	switch s {
	case "s2", "s3":
		return LOCKED
	case "s0", "s1":
		return FREERUN
	default:
		return FREERUN
	}
}

// removeMessageSuffix removes log severity suffixes from log messages
func removeMessageSuffix(input string) (output string) {
	// container log output  "ptp4l[2464681.628]: [phc2sys.1.config:7] master offset -4 s2 freq -26835 path delay 525"
	// make sure non-supported version can handle suffix tags
	// clear {} from unparsed template
	//"ptp4l[2464681.628]: [phc2sys.1.config:{level}] master offset -4 s2 freq -26835 path delay 525"
	replacer := strings.NewReplacer("{", "", "}", "")
	output = replacer.Replace(input)
	// Replace matching parts in the input string
	output = messageTagSuffixRegEx.ReplaceAllString(output, "$1")
	return output
}

// validateFloat parses a string to float64 and returns an error if parsing fails
func validateFloat(value, fieldName string) (float64, error) {
	if value == "" {
		return 0, nil
	}
	result, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse %s: %v", fieldName, err)
	}
	return result, nil
}

// validateInt parses a string to int and returns an error if parsing fails
func validateInt(value, fieldName string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("%s is required", fieldName)
	}
	result, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("failed to parse %s: %v", fieldName, err)
	}
	return result, nil
}

// validateRequired checks if a required field is present
func validateRequired(value, fieldName string) error {
	if value == "" {
		return fmt.Errorf("%s is required", fieldName)
	}
	return nil
}

// extractNamedGroups extracts named groups from a regex match
func extractNamedGroups(regex *regexp.Regexp, input string) map[string]string {
	match := regex.FindStringSubmatch(input)
	if match == nil {
		return nil
	}
	result := map[string]string{}
	for i, name := range regex.SubexpNames() {
		if i > 0 && name != "" {
			result[name] = match[i]
		}
	}
	return result
}
