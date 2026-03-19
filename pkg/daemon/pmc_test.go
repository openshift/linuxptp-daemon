package daemon_test

import (
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	expect "github.com/google/goexpect"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/daemon"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
)

// discardWriteCloser absorbs all writes without blocking (used as SpawnGeneric In).
type discardWriteCloser struct{}

func (discardWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (discardWriteCloser) Close() error                { return nil }

// testPMCMonitor provides mock getMonitor and poll functions for PMCProcess tests.
type testPMCMonitor struct {
	t         *testing.T
	killCh    chan struct{}
	pollCount atomic.Int32
	calls     atomic.Int32
	pmc       *daemon.PMCProcess
	maxPolls  int32
}

//nolint:unparam
func newTestPMCMonitor(t *testing.T, maxPolls int32) *testPMCMonitor {
	return &testPMCMonitor{
		t:        t,
		killCh:   make(chan struct{}),
		maxPolls: maxPolls,
	}
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

func (m *testPMCMonitor) poll(_ string, _ bool) (protocol.ParentDataSet, error) {
	count := m.pollCount.Add(1)
	if count > m.maxPolls {
		m.t.Errorf("runaway detected: poll called %d times, orphaned expectWorker spinning", count)
		m.pmc.CmdStop()
	}
	return protocol.ParentDataSet{}, fmt.Errorf("test: poll disabled")
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
	pmc := daemon.NewTestPMCProcess("test.config", "T-BC", mock.getMonitor, mock.poll)
	mock.pmc = pmc

	pmc.CmdRun(false)

	waitFor(t, 5*time.Second, "poll called at least once", func() bool {
		return mock.pollCount.Load() > 0
	})

	pmc.CmdStop()

	waitFor(t, 5*time.Second, "process stopped", func() bool {
		return pmc.Stopped()
	})
}

func TestMonitorNoOrphanAfterProcessDeath(t *testing.T) {
	mock := newTestPMCMonitor(t, 50)
	pmc := daemon.NewTestPMCProcess("test.config", "T-BC", mock.getMonitor, mock.poll)
	mock.pmc = pmc

	pmc.CmdRun(false)

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

	pmc.CmdStop()
	waitFor(t, 5*time.Second, "process stopped", func() bool {
		return pmc.Stopped()
	})
}

func TestCmdStopIdempotent(t *testing.T) {
	mock := newTestPMCMonitor(t, 50)
	pmc := daemon.NewTestPMCProcess("test.config", "T-BC", mock.getMonitor, mock.poll)
	mock.pmc = pmc

	pmc.CmdRun(false)

	waitFor(t, 5*time.Second, "poll called at least once", func() bool {
		return mock.pollCount.Load() > 0
	})

	pmc.CmdStop()
	pmc.CmdStop()
	pmc.CmdStop()

	waitFor(t, 5*time.Second, "process stopped", func() bool {
		return pmc.Stopped()
	})
}

func TestCmdStopBeforeCmdRun(t *testing.T) {
	mock := newTestPMCMonitor(t, 50)
	pmc := daemon.NewTestPMCProcess("test.config", "T-BC", mock.getMonitor, mock.poll)
	mock.pmc = pmc

	pmc.CmdStop()

	if !pmc.Stopped() {
		t.Error("expected process to be stopped after CmdStop")
	}

	// CmdRun after CmdStop should return early without starting monitoring
	pmc.CmdRun(false)

	time.Sleep(500 * time.Millisecond)

	if mock.pollCount.Load() > 0 {
		t.Error("CmdRun should not have started monitoring after CmdStop")
	}
}
