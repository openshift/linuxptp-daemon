package logfilter_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/logfilter"
	"github.com/stretchr/testify/assert"
)

type logFilterTestCase struct {
	testName       string
	processName    string
	messageTag     string
	ptpSettings    map[string]string
	logs           []string
	expectedOutput string
}

func initEnhancedLogFilterTestCases() []logFilterTestCase {
	testLogs := []string{
		"ptp4l[6059353.707]: [ptp4l.0.config:6] master offset        -10 s2 freq  -12461 path delay      1512",
		"ptp4l[6059353.770]: [ptp4l.0.config:6] master offset         -7 s2 freq  -12457 path delay      1511",
		"ptp4l[6059353.832]: [ptp4l.0.config:6] master offset          2 s2 freq  -12442 path delay      1511",
		"ptp4l[6059353.895]: [ptp4l.0.config:6] master offset          2 s2 freq  -12442 path delay      1512",
		"ptp4l[6059353.958]: [ptp4l.0.config:6] master offset         -4 s2 freq  -12452 path delay      1512",
		"ptp4l[6059354.020]: [ptp4l.0.config:6] master offset         -7 s2 freq  -12458 path delay      1513",
		"ptp4l[6059354.082]: [ptp4l.0.config:6] master offset         -3 s2 freq  -12451 path delay      1513",
		"ptp4l[6059354.146]: [ptp4l.0.config:6] master offset         -3 s2 freq  -12452 path delay      1512",
		"ptp4l[6059354.208]: [ptp4l.0.config:6] master offset         -9 s2 freq  -12462 path delay      1512",
		"ptp4l[6059354.270]: [ptp4l.0.config:6] master offset          1 s2 freq  -12446 path delay      1512",
		"ptp4l[6059354.344]: [ptp4l.0.config:6] master offset          1 s2 freq  -12446 path delay      1513",
		"ptp4l[6059354.395]: [ptp4l.0.config:6] master offset         -4 s2 freq  -12454 path delay      1513",
		"ptp4l[6059354.459]: [ptp4l.0.config:6] master offset         -3 s2 freq  -12453 path delay      1513",
		"ptp4l[6059354.520]: [ptp4l.0.config:6] master offset         -5 s2 freq  -12457 path delay      1513",
		"ptp4l[6059354.583]: [ptp4l.0.config:6] master offset         -3 s2 freq  -12454 path delay      1512",
		"ptp4l[6059354.646]: [ptp4l.0.config:6] master offset          4 s2 freq  -12442 path delay      1511",
		"phc2sys[6059354.693]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset         8 s2 freq  +11015 delay    489",
		"ptp4l[6059354.708]: [ptp4l.0.config:6] master offset          1 s2 freq  -12447 path delay      1513",
		"ptp4l[6059354.770]: [ptp4l.0.config:6] master offset         -7 s2 freq  -12461 path delay      1513",
		"ptp4l[6059354.833]: [ptp4l.0.config:6] master offset         -4 s2 freq  -12456 path delay      1513",
		"ptp4l[6059354.896]: [ptp4l.0.config:6] master offset         -3 s2 freq  -12455 path delay      1513",
		"ptp4l[6059354.958]: [ptp4l.0.config:6] master offset         -3 s2 freq  -12455 path delay      1513",
		"ptp4l[6059355.020]: [ptp4l.0.config:6] master offset         -6 s2 freq  -12461 path delay      1512",
		"ptp4l[6059355.083]: [ptp4l.0.config:6] master offset         -5 s2 freq  -12459 path delay      1512",
		"ptp4l[6059355.159]: [ptp4l.0.config:6] master offset          2 s2 freq  -12448 path delay      1512",
		"ptp4l[6059355.209]: [ptp4l.0.config:6] master offset          4 s2 freq  -12444 path delay      1512",
		"ptp4l[6059355.271]: [ptp4l.0.config:6] master offset          4 s2 freq  -12444 path delay      1512",
		"ptp4l[6059355.333]: [ptp4l.0.config:6] master offset          4 s2 freq  -12444 path delay      1513",
		"ptp4l[6059355.396]: [ptp4l.0.config:6] master offset         -7 s2 freq  -12462 path delay      1513",
		"ptp4l[6059355.458]: [ptp4l.0.config:6] master offset         -3 s2 freq  -12456 path delay      1513",
		"ptp4l[6059355.521]: [ptp4l.0.config:6] master offset          4 s2 freq  -12444 path delay      1512",
		"ptp4l[6059355.583]: [ptp4l.0.config:6] master offset          4 s2 freq  -12444 path delay      1512",
		"ptp4l[6059355.647]: [ptp4l.0.config:6] master offset         -1 s2 freq  -12452 path delay      1513",
		"phc2sys[6059355.694]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset        -3 s2 freq  +11006 delay    499",
		"ptp4l[6059355.708]: [ptp4l.0.config:6] master offset          7 s2 freq  -12438 path delay      1513",
		"ptp4l[6059355.771]: [ptp4l.0.config:6] master offset          1 s2 freq  -12448 path delay      1513",
		"ptp4l[6059355.833]: [ptp4l.0.config:6] master offset          3 s2 freq  -12444 path delay      1513",
		"ptp4l[6059355.896]: [ptp4l.0.config:6] master offset          7 s2 freq  -12437 path delay      1513",
		"ptp4l[6059355.958]: [ptp4l.0.config:6] master offset          1 s2 freq  -12447 path delay      1513",
		"ptp4l[6059356.021]: [ptp4l.0.config:6] master offset          6 s2 freq  -12438 path delay      1512",
		"ptp4l[6059356.084]: [ptp4l.0.config:6] master offset          7 s2 freq  -12436 path delay      1513",
		"ptp4l[6059356.146]: [ptp4l.0.config:6] master offset          0 s2 freq  -12447 path delay      1513",
		"ptp4l[6059356.208]: [ptp4l.0.config:6] master offset          3 s2 freq  -12442 path delay      1513",
		"ptp4l[6059356.271]: [ptp4l.0.config:6] master offset          6 s2 freq  -12437 path delay      1513",
		"ptp4l[6059356.334]: [ptp4l.0.config:6] master offset          6 s2 freq  -12436 path delay      1513",
		"ptp4l[6059356.396]: [ptp4l.0.config:6] master offset         12 s2 freq  -12425 path delay      1513",
		"ptp4l[6059356.459]: [ptp4l.0.config:6] master offset         13 s2 freq  -12422 path delay      1513",
		"ptp4l[6059356.521]: [ptp4l.0.config:6] master offset          0 s2 freq  -12443 path delay      1514",
		"ptp4l[6059356.584]: [ptp4l.0.config:6] master offset          4 s2 freq  -12436 path delay      1514",
		"ptp4l[6059356.647]: [ptp4l.0.config:6] master offset          1 s2 freq  -12441 path delay      1513",
		"phc2sys[6059356.694]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset       -14 s2 freq  +10994 delay    493",
	}
	onelineTestLogs := []string{
		"phc2sys[6059354.693]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset         -10 s2 freq  +11015 delay    489",
		"ptp4l[6059354.708]: [ptp4l.0.config:6] master offset          -3 s2 freq  -12447 path delay      1513",
	}
	malformedTestLogs := []string{
		"phc2sys[6059354.693]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset         notanint s2 freq  +11015 delay    489",
		"ptp4l[6059354.708]: [ptp4l.0.config:6] master offset          notanint s2 freq  -12447 path delay      1513",
	}
	emptyTestLogs := []string{}
	ret := []logFilterTestCase{
		{
			testName:    "enhanced ptp4l",
			processName: "ptp4l",
			messageTag:  "[ptp4l.0.config:{level}]",
			ptpSettings: map[string]string{
				"logReduce": "enhanced",
			},
			logs:           testLogs,
			expectedOutput: " [ptp4l.0.config:6] master offset summary: cnt=48, min=-10, max=13, avg=0.27, SD=5.23",
		},
		{
			testName:    "enhanced phc2sys",
			processName: "phc2sys",
			messageTag:  "[ptp4l.0.config:{level}]",
			ptpSettings: map[string]string{
				"logReduce": "enhanced",
			},
			logs:           testLogs,
			expectedOutput: " [ptp4l.0.config:6] phc offset summary: cnt=3, min=-14, max=8, avg=-3.00, SD=11.00",
		},
		{
			testName:    "malformed ptp4l",
			processName: "ptp4l",
			messageTag:  "[ptp4l.0.config:{level}]",
			ptpSettings: map[string]string{
				"logReduce": "enhanced",
			},
			logs:           malformedTestLogs,
			expectedOutput: " [ptp4l.0.config:6] master offset summary: cnt=0, min=0, max=0, avg=NaN, SD=NaN",
		},
		{
			testName:    "malformed phc2sys",
			processName: "phc2sys",
			messageTag:  "[ptp4l.0.config:{level}]",
			ptpSettings: map[string]string{
				"logReduce": "enhanced",
			},
			logs:           malformedTestLogs,
			expectedOutput: " [ptp4l.0.config:6] phc offset summary: cnt=0, min=0, max=0, avg=NaN, SD=NaN",
		},
		{
			testName:    "oneline ptp4l",
			processName: "ptp4l",
			messageTag:  "[ptp4l.0.config:{level}]",
			ptpSettings: map[string]string{
				"logReduce": "enhanced",
			},
			logs:           onelineTestLogs,
			expectedOutput: " [ptp4l.0.config:6] master offset summary: cnt=1, min=-3, max=-3, avg=-3.00, SD=NaN",
		},
		{
			testName:    "oneline phc2sys",
			processName: "phc2sys",
			messageTag:  "[ptp4l.0.config:{level}]",
			ptpSettings: map[string]string{
				"logReduce": "enhanced",
			},
			logs:           onelineTestLogs,
			expectedOutput: " [ptp4l.0.config:6] phc offset summary: cnt=1, min=-10, max=-10, avg=-10.00, SD=NaN",
		},
		{
			testName:    "empty ptp4l",
			processName: "ptp4l",
			messageTag:  "[ptp4l.0.config:{level}]",
			ptpSettings: map[string]string{
				"logReduce": "enhanced",
			},
			logs:           emptyTestLogs,
			expectedOutput: " [ptp4l.0.config:6] master offset summary: cnt=0, min=0, max=0, avg=NaN, SD=NaN",
		},
		{
			testName:    "empty phc2sys",
			processName: "phc2sys",
			messageTag:  "[ptp4l.0.config:{level}]",
			ptpSettings: map[string]string{
				"logReduce": "enhanced",
			},
			logs:           emptyTestLogs,
			expectedOutput: " [ptp4l.0.config:6] phc offset summary: cnt=0, min=0, max=0, avg=NaN, SD=NaN",
		},
	}
	return ret
}

func TestDaemon_LogFilterEnhanced(t *testing.T) {
	testCases := initEnhancedLogFilterTestCases()
	for _, tc := range testCases {
		logFilters := logfilter.GetLogFilters(tc.processName, tc.messageTag, tc.ptpSettings)
		for _, line := range tc.logs {
			logfilter.FilterOutput(logFilters, line)
		}
		actualOutput := strings.SplitN(logFilters[0].FlushOutput(), ":", 2)[1]

		assert.Equal(t, tc.expectedOutput, actualOutput, fmt.Sprintf("Rendered output doesn't match expected: %s", tc.testName))
	}
}
