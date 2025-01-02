package debug_test

import (
	"github.com/openshift/linuxptp-daemon/pkg/debug"
	"testing"
)

func TestPrintALLState_ChangesStateAndSetsResetGM(t *testing.T) {
	debug.UpdateGMState("s2")
	debug.UpdateDPLLState("s1", 100, "eth0")
	debug.UpdateDPLLState("s1", 100, "eth1")
	debug.UpdateGNSSState("s1", 100)
	debug.UpdateTs2phcState("s1", 100, "eth0")
	debug.UpdateTs2phcState("s1", 100, "eth2")
	debug.UpdateDPLLState("s1", 0, debug.OverallDpllKey)
	debug.UpdateTs2phcState("s1", 0, debug.OverallTs2phcKey)
	debug.UpdateClockClass(2)
	debug.PrintTree()
}
