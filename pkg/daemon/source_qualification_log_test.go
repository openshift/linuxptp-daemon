package daemon

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/require"
)

func TestSourceQualificationTransitionLogs(t *testing.T) {
	// Isolate glog flags/output from the other tests and their background workers.
	if os.Getenv("PTP_QUALIFICATION_LOG_CHILD") != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSourceQualificationTransitionLogs$", "-logtostderr=true")
		cmd.Env = append(os.Environ(), "PTP_QUALIFICATION_LOG_CHILD=1")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		var transitions []string
		for _, line := range strings.Split(string(output), "\n") {
			if strings.Contains(line, "PTPSourceQualified profile=source-log-test") {
				transitions = append(transitions, line)
			}
		}
		require.Len(t, transitions, 2, "unchanged samples must not repeat transition logs: %s", output)
		for _, field := range []string{"config=ptp4l.0.config", "port=tr0", "qualified=true", "samples=2", `reason="offset window"`} {
			require.Contains(t, transitions[0], field)
		}
		for _, field := range []string{"qualified=false", "samples=1", `reason="offset window"`} {
			require.Contains(t, transitions[1], field)
		}
		return
	}
	p := &ptpProcess{
		ExecProcess:         ExecProcess{name: ptp4lProcessName, configName: "ptp4l.0.config", eventCh: make(chan event.Event, 8)},
		nodeProfile:         &ptpv1.PtpProfile{Name: utils.ToPtr("source-log-test")},
		tBCAttributes:       tBCProcessAttributes{activePort: "tr0", trIfaceNames: []string{"tr0"}, perPortState: map[string]event.PTPState{"tr0": event.PTP_LOCKED}},
		sourceQualification: &sourceQualificationRuntime{apply: func(bool) error { return nil }},
	}
	attachQualificationClock(t, p, utils.QualificationConfig{QualifiedThreshold: 100, DisqualifiedThreshold: 200, QualifiedSamples: 2, DisqualifiedSamples: 1})
	p.forwardSourceData(nil, &parser.PTPEvent{Iface: "tr0", Role: parserconstants.PortRoleSlave})
	for _, offset := range []float64{10, 10, 10, 201, 201} {
		p.forwardSourceData(&parser.Metrics{Iface: "tr0", Offset: offset, ServoState: "s2"}, nil)
	}
	glog.Flush()
}
