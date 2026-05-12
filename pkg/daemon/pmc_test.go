package daemon_test

import (
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/daemon"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/pmc"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

// discardWriteCloser absorbs all writes without blocking (used as SpawnGeneric In).
type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

// testPMCMonitor provides mock getMonitor and a MockClient for PMCProcess tests.
type testPMCMonitor struct {
	t         *testing.T
	killCh    chan struct{}
	pollCount atomic.Int32
	calls     atomic.Int32
	pmc       *daemon.PMCProcess
	maxPolls  int32
	mock      *pmc.MockClient
}

//nolint:unparam
func newTestPMCMonitor(t *testing.T, maxPolls int32) *testPMCMonitor {
	m := &testPMCMonitor{
		t:        t,
		killCh:   make(chan struct{}),
		maxPolls: maxPolls,
	}
	m.mock = &pmc.MockClient{
		ParentDSErr: fmt.Errorf("test: poll disabled"),
	}
	return m
}

func (m *testPMCMonitor) client() pmc.Client {
	return &countingPMCClient{MockClient: m.mock, monitor: m}
}

func (m *testPMCMonitor) getMonitor(_ string) (*expect.GExpect, <-chan error, error) {
	call := m.calls.Add(1)

	pr, pw := io.Pipe()

	alive := &atomic.Bool{}
	alive.Store(true)
	doneCh := make(chan struct{})

	exp, errCh, err := expect.SpawnGeneric(&expect.GenOptions{
		In:  discardWriteCloser{},
		Out: pr,
		Wait: func() error {
			<-doneCh
			return nil
		},
		Close: func() error {
			alive.Store(false)
			select {
			case <-doneCh:
			default:
				close(doneCh)
			}
			return pw.Close()
		},
		Check: func() bool { return alive.Load() },
	}, 10*time.Minute, expect.CheckDuration(100*time.Millisecond))
	if err != nil {
		return nil, nil, err
	}

	// Only the first monitor instance gets a kill trigger
	if call == 1 {
		go func() {
			<-m.killCh
			exp.Close()
		}()
	}

	return exp, errCh, nil
}

// countingPMCClient wraps a MockClient and counts GetParentDS calls to detect runaway polling.
type countingPMCClient struct {
	*pmc.MockClient
	monitor *testPMCMonitor
}

func (c *countingPMCClient) GetParentDS(cfgName string) (protocol.ParentDataSet, error) {
	count := c.monitor.pollCount.Add(1)
	if count > c.monitor.maxPolls {
		c.monitor.t.Errorf("runaway detected: poll called %d times, orphaned expectWorker spinning", count)
		c.monitor.pmc.CmdStop()
	}
	return c.MockClient.GetParentDS(cfgName)
}

//nolint:unparam
func waitFor(t *testing.T, timeout time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for !cond() {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for: %s", desc)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestMonitorExitsViaCmdStop(t *testing.T) {
	mock := newTestPMCMonitor(t, 50)
	pmc.SetMock(mock.client())
	defer pmc.ResetMock()
	proc := daemon.NewTestPMCProcess("test.config", "T-BC", mock.getMonitor)
	mock.pmc = proc

	proc.CmdRun(false)

	waitFor(t, 5*time.Second, "poll called at least once", func() bool {
		return mock.pollCount.Load() > 0
	})

	proc.CmdStop()

	waitFor(t, 5*time.Second, "process stopped", func() bool {
		return proc.Stopped()
	})
}

func TestMonitorNoOrphanAfterProcessDeath(t *testing.T) {
	mock := newTestPMCMonitor(t, 50)
	pmc.SetMock(mock.client())
	defer pmc.ResetMock()
	proc := daemon.NewTestPMCProcess("test.config", "T-BC", mock.getMonitor)
	mock.pmc = proc

	proc.CmdRun(false)

	waitFor(t, 5*time.Second, "poll called at least once", func() bool {
		return mock.pollCount.Load() > 0
	})

	// Simulate process death
	close(mock.killCh)

	// Wait for death to propagate through CloseExpect (~500ms) and monitor restart
	time.Sleep(2 * time.Second)

	countAfterKill := mock.pollCount.Load()

	// An orphaned expectWorker tight-looping would cause poll count to grow rapidly
	time.Sleep(1 * time.Second)
	countFinal := mock.pollCount.Load()

	// The restarted monitor may trigger a few polls, but an orphan would cause hundreds
	delta := countFinal - countAfterKill
	if delta > 5 {
		t.Errorf("expectWorker appears orphaned: poll count grew by %d after process death (from %d to %d)",
			delta, countAfterKill, countFinal)
	}

	proc.CmdStop()
	waitFor(t, 5*time.Second, "process stopped", func() bool {
		return proc.Stopped()
	})
}

func TestCmdStopIdempotent(t *testing.T) {
	mock := newTestPMCMonitor(t, 50)
	pmc.SetMock(mock.client())
	defer pmc.ResetMock()
	proc := daemon.NewTestPMCProcess("test.config", "T-BC", mock.getMonitor)
	mock.pmc = proc

	proc.CmdRun(false)

	waitFor(t, 5*time.Second, "poll called at least once", func() bool {
		return mock.pollCount.Load() > 0
	})

	proc.CmdStop()
	proc.CmdStop()
	proc.CmdStop()

	waitFor(t, 5*time.Second, "process stopped", func() bool {
		return proc.Stopped()
	})
}

func TestCmdStopBeforeCmdRun(t *testing.T) {
	mock := newTestPMCMonitor(t, 50)
	pmc.SetMock(mock.client())
	defer pmc.ResetMock()
	proc := daemon.NewTestPMCProcess("test.config", "T-BC", mock.getMonitor)
	mock.pmc = proc

	proc.CmdStop()

	if !proc.Stopped() {
		t.Error("expected process to be stopped after CmdStop")
	}

	proc.CmdRun(false)

	time.Sleep(500 * time.Millisecond)

	if mock.pollCount.Load() > 0 {
		t.Error("CmdRun should not have started monitoring after CmdStop")
	}
}
