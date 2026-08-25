package daemon

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
)

// TestReplayDualUpstreamLog replays the actual ptp4l log sequence from OCPBUGS-111881
// and verifies openshift_ptp_interface_role metrics after each phase.
//
// Sequence observed on the node (log-dual-upstream.txt):
//
//	Initial boot:
//	  port 1 (eno8303): INITIALIZING → LISTENING → UNCALIBRATED → SLAVE
//	  port 2 (eno8403): INITIALIZING → LISTENING → PRE_MASTER → MASTER
//
//	Switchover (eno8303 link down):
//	  port 1 (eno8303): SLAVE → FAULTY
//	  port 2 (eno8403): MASTER → UNCALIBRATED → [SLAVE if eno8303 doesn't recover fast enough]
//
//	Recovery (eno8303 link up):
//	  port 1 (eno8303): FAULTY → LISTENING → UNCALIBRATED → SLAVE
//	  port 2 (eno8403): SLAVE → PRE_MASTER → MASTER
func TestReplayDualUpstreamLog(t *testing.T) {
	InitializeOffsetMaps()

	handler := event.Init("test-node", false, "", nil, nil, nil, nil, nil)
	process := &ptpProcess{
		name:       ptp4lProcessName,
		messageTag: "[ptp4l.1.config]",
		ifaces: config.IFaces{
			{Name: "eno8303"},
			{Name: "eno8403"},
		},
		handler:   handler,
		logParser: parser.NewPTP4LExtractor(),
	}

	// --- Phase 1: Initial boot ---
	// ptp4l elects eno8303 as SLAVE, eno8403 becomes MASTER
	bootLines := []string{
		"ptp4l[82484.979]: [ptp4l.1.config:5] port 1 (eno8303): INITIALIZING to LISTENING on INIT_COMPLETE",
		"ptp4l[82484.999]: [ptp4l.1.config:5] port 2 (eno8403): INITIALIZING to LISTENING on INIT_COMPLETE",
		"ptp4l[82485.340]: [ptp4l.1.config:5] port 1 (eno8303): LISTENING to UNCALIBRATED on RS_SLAVE",
		"ptp4l[82485.340]: [ptp4l.1.config:5] port 2 (eno8403): LISTENING to PRE_MASTER on RS_MASTER",
		"ptp4l[82485.590]: [ptp4l.1.config:5] port 2 (eno8403): PRE_MASTER to MASTER on QUALIFICATION_TIMEOUT_EXPIRES",
		"ptp4l[82533.101]: [ptp4l.1.config:5] port 1 (eno8303): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
	}
	for _, line := range bootLines {
		processWithParser(process, line)
	}
	assert.Equal(t, float64(SLAVE), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8303")), "boot: eno8303 should be SLAVE")
	assert.Equal(t, float64(MASTER), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8403")), "boot: eno8403 should be MASTER")

	// --- Phase 2: Switchover (eno8303 link down) ---
	// eno8403 transitions MASTER → UNCALIBRATED (BMCA re-eval, not FAULTY) → SLAVE
	switchoverLines := []string{
		"ptp4l[82736.439]: [ptp4l.1.config:5] port 1 (eno8303): SLAVE to FAULTY on FAULT_DETECTED (FT_UNSPECIFIED)",
		"ptp4l[82736.000]: [ptp4l.1.config:5] port 2 (eno8403): MASTER to UNCALIBRATED on RS_SLAVE",
		"ptp4l[82737.000]: [ptp4l.1.config:5] port 2 (eno8403): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
	}
	for _, line := range switchoverLines {
		processWithParser(process, line)
	}
	assert.Equal(t, float64(FAULTY), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8303")), "switchover: eno8303 should be FAULTY")
	assert.Equal(t, float64(SLAVE), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8403")), "switchover: eno8403 should be SLAVE")

	// --- Phase 3: Recovery (eno8303 link up) ---
	// ptp4l logs all transitions; eno8403 goes SLAVE → PRE_MASTER → MASTER as eno8303 takes over
	recoveryLines := []string{
		"ptp4l[82736.439]: [ptp4l.1.config:5] port 1 (eno8303): FAULTY to LISTENING on INIT_COMPLETE",
		"ptp4l[82736.774]: [ptp4l.1.config:5] port 1 (eno8303): LISTENING to UNCALIBRATED on RS_SLAVE",
		"ptp4l[82736.774]: [ptp4l.1.config:5] port 2 (eno8403): SLAVE to PRE_MASTER on RS_MASTER",
		"ptp4l[82737.024]: [ptp4l.1.config:5] port 2 (eno8403): PRE_MASTER to MASTER on QUALIFICATION_TIMEOUT_EXPIRES",
		"ptp4l[82752.903]: [ptp4l.1.config:5] port 1 (eno8303): UNCALIBRATED to SLAVE on MASTER_CLOCK_SELECTED",
	}
	for _, line := range recoveryLines {
		processWithParser(process, line)
	}
	assert.Equal(t, float64(SLAVE), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8303")), "recovery: eno8303 should be SLAVE")
	assert.Equal(t, float64(MASTER), testutil.ToFloat64(InterfaceRole.WithLabelValues(ptp4lProcessName, NodeName, "eno8403")), "recovery: eno8403 should be MASTER")
}
