package parser

import "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"

// parseClockState converts PTP state strings to clock state constants
func parseClockState(s string) constants.ClockState {
	switch s {
	case "s2", "s3":
		return constants.ClockStateLocked
	case "s0", "s1":
		return constants.ClockStateFreeRun
	default:
		return constants.ClockStateFreeRun
	}
}
