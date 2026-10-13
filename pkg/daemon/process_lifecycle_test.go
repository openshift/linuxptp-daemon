package daemon

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/process"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockingStartCmd struct {
	*MockCmd
	startCalled  chan struct{}
	startRelease chan struct{}
	started      atomic.Bool
	calledEarly  atomic.Bool
}

func newBlockingStartCmd() *blockingStartCmd {
	return &blockingStartCmd{
		MockCmd:      NewMockCmd(),
		startCalled:  make(chan struct{}),
		startRelease: make(chan struct{}),
	}
}

func (c *blockingStartCmd) Start() error {
	close(c.startCalled)
	<-c.startRelease
	c.started.Store(true)
	return nil
}

func (c *blockingStartCmd) Pid() int {
	if !c.started.Load() {
		c.calledEarly.Store(true)
	}
	return c.MockCmd.Pid()
}

func (c *blockingStartCmd) Signal(sig os.Signal) error {
	if !c.started.Load() {
		c.calledEarly.Store(true)
	}
	return c.MockCmd.Signal(sig)
}

func (c *blockingStartCmd) Clone() ProcessCmd { return c }

const (
	ptp4lConfig = "ptp4l.0.config"
)

func waitProcessState(t *testing.T, p process.Process, want process.State) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if p.State() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for state %v, got %v", want, p.State())
}

func waitProcessStatus(t *testing.T, ch <-chan event.Event, want int64) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ps, ok := ev.Data.(*event.ProcessStatusData); ok && ps.Status == want {
				return
			}
		case <-deadline:
			t.Fatalf("timeout waiting for PROCESS_STATUS %d", want)
		}
	}
}

func newTestPtpProcess(cmd ProcessCmd, ch chan event.Event) *ptpProcess {
	return &ptpProcess{
		ExecProcess: ExecProcess{
			name:       ptp4lProcessName,
			configName: ptp4lConfig,
			eventCh:    ch,
		},
		cmd: cmd,
		dn:  &Daemon{},
	}
}

func TestPtpProcessStartGates(t *testing.T) {
	p := newTestPtpProcess(NewMockCmd(), nil)
	p.state = process.Starting
	require.NoError(t, p.Start(context.Background()))
	assert.Equal(t, process.Starting, p.State())

	p.state = process.Running
	require.NoError(t, p.Start(context.Background()))
	assert.Equal(t, process.Running, p.State())

	p.state = process.Stopping
	err := p.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is stopping")
}

func TestPtpProcessStartStopRestart(t *testing.T) {
	ch := make(chan event.Event, 8)
	p := newTestPtpProcess(NewMockCmd(), ch)

	require.NoError(t, p.Start(context.Background()))
	waitProcessState(t, p, process.Running)
	waitProcessStatus(t, ch, PtpProcessUp)
	require.NoError(t, p.Start(context.Background()))
	assert.Equal(t, process.Running, p.State())

	firstStopCh := p.stopCh
	firstCmd := p.cmd
	require.NoError(t, p.Stop())
	waitProcessState(t, p, process.Stopped)
	waitProcessStatus(t, ch, PtpProcessDown)

	require.NoError(t, p.Stop())
	assert.Equal(t, process.Stopped, p.State())

	require.NoError(t, p.Start(context.Background()))
	waitProcessState(t, p, process.Running)
	waitProcessStatus(t, ch, PtpProcessUp)
	assert.NotEqual(t, firstStopCh, p.stopCh)
	assert.NotEqual(t, firstCmd, p.cmd)
	select {
	case <-p.stopCh:
		t.Fatal("stopCh should be open after Start")
	default:
	}

	require.NoError(t, p.Stop())
	waitProcessState(t, p, process.Stopped)
}

func TestPtpProcessStopWaitsForStart(t *testing.T) {
	cmd := newBlockingStartCmd()
	p := newTestPtpProcess(cmd, make(chan event.Event, 4))

	require.NoError(t, p.Start(context.Background()))
	<-cmd.startCalled

	stopDone := make(chan error, 1)
	go func() { stopDone <- p.Stop() }()
	select {
	case err := <-stopDone:
		require.NoError(t, err)
		t.Fatal("Stop returned before Start completed")
	case <-time.After(100 * time.Millisecond):
	}
	require.False(t, cmd.calledEarly.Load())

	close(cmd.startRelease)
	require.NoError(t, <-stopDone)
	waitProcessState(t, p, process.Stopped)
	require.False(t, cmd.calledEarly.Load())
}

func TestPtpProcessStopBeforeCmdStart(t *testing.T) {
	cmd := newBlockingStartCmd()
	p := newTestPtpProcess(cmd, nil)
	p.state = process.Starting
	p.stopCh = make(chan struct{})

	require.NoError(t, p.Stop())
	require.Equal(t, process.Stopping, p.State())
	require.False(t, cmd.calledEarly.Load())
	select {
	case <-p.stopCh:
	default:
		t.Fatal("stopCh should be closed")
	}
}

func TestPtpProcessCrashDead(t *testing.T) {
	ch := make(chan event.Event, 8)
	p := newTestPtpProcess(NewDeadMockCmd(), ch)

	require.NoError(t, p.Start(context.Background()))
	waitProcessState(t, p, process.Dead)
	waitProcessStatus(t, ch, PtpProcessDown)
}

func TestPtpProcessNilCmdDead(t *testing.T) {
	ch := make(chan event.Event, 8)
	p := newTestPtpProcess(nil, ch)

	require.NoError(t, p.Start(context.Background()))
	waitProcessState(t, p, process.Dead)
	waitProcessStatus(t, ch, PtpProcessDown)
}

func TestPtpProcessStopCreatedNoop(t *testing.T) {
	p := newTestPtpProcess(NewMockCmd(), nil)
	require.NoError(t, p.Stop())
	assert.Equal(t, process.Created, p.State())
}

func TestPtpProcessStopDoesNotClearDepProcess(t *testing.T) {
	p := newTestPtpProcess(NewMockCmd(), nil)
	dep := &stubProcess{name: "gpsd"}
	p.depProcess = []process.Process{dep}

	require.NoError(t, p.Start(context.Background()))
	waitProcessState(t, p, process.Running)

	require.NoError(t, p.Stop())
	require.Len(t, p.depProcess, 1)
	assert.Same(t, dep, p.depProcess[0])
}

func TestGpsdStartStopRestart(t *testing.T) {
	ch := make(chan event.Event, 8)
	g := NewTestGpsdProcess("", "", "", ch, NewMockCmd())
	g.setState(process.Dead)

	require.NoError(t, g.Start(context.Background()))
	waitProcessState(t, g, process.Running)
	waitProcessStatus(t, ch, PtpProcessUp)

	firstStopCh := g.stopCh
	require.NoError(t, g.Stop())
	waitProcessState(t, g, process.Stopped)
	waitProcessStatus(t, ch, PtpProcessDown)

	require.NoError(t, g.Start(context.Background()))
	waitProcessState(t, g, process.Running)
	assert.NotEqual(t, firstStopCh, g.stopCh)
	select {
	case <-g.stopCh:
		t.Fatal("stopCh should be open after Start")
	default:
	}
	require.NoError(t, g.Stop())
	waitProcessState(t, g, process.Stopped)
}

func TestGpsdStartGates(t *testing.T) {
	g := NewTestGpsdProcess("", "", "", nil, nil)
	g.setState(process.Starting)
	require.NoError(t, g.Start(context.Background()))
	assert.Equal(t, process.Starting, g.State())

	g.setState(process.Running)
	require.NoError(t, g.Start(context.Background()))
	assert.Equal(t, process.Running, g.State())

	g.setState(process.Stopping)
	err := g.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is stopping")
}

func TestGpsdCrashDead(t *testing.T) {
	ch := make(chan event.Event, 8)
	g := NewTestGpsdProcess("", "", "", ch, NewDeadMockCmd())
	g.setState(process.Dead)
	require.NoError(t, g.Start(context.Background()))
	waitProcessState(t, g, process.Dead)
	waitProcessStatus(t, ch, PtpProcessDown)
}

func TestGpspipeNilCmdNoPanic(t *testing.T) {
	ch := make(chan event.Event, 8)
	gp := NewTestGpsPipeProcess("", ch, nil, "", "")
	require.NotPanics(t, func() {
		require.NoError(t, gp.Start(context.Background()))
	})
	waitProcessState(t, gp, process.Dead)
	waitProcessStatus(t, ch, PtpProcessDown)
}

func TestGpspipeStartGates(t *testing.T) {
	gp := NewTestGpsPipeProcess("", nil, nil, "", "")
	gp.setState(process.Starting)
	require.NoError(t, gp.Start(context.Background()))
	assert.Equal(t, process.Starting, gp.State())

	gp.setState(process.Running)
	require.NoError(t, gp.Start(context.Background()))
	assert.Equal(t, process.Running, gp.State())

	gp.setState(process.Stopping)
	err := gp.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is stopping")
}

func TestPMCStartGates(t *testing.T) {
	p := &PMCProcess{}
	p.processState = process.Starting
	require.NoError(t, p.Start(context.Background()))
	assert.Equal(t, process.Starting, p.State())

	p.processState = process.Running
	require.NoError(t, p.Start(context.Background()))
	assert.Equal(t, process.Running, p.State())

	p.processState = process.Stopping
	err := p.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is stopping")
}

func TestPMCStopCreatedIsStopped(t *testing.T) {
	p := &PMCProcess{}
	require.NoError(t, p.Stop())
	assert.True(t, p.Stopped())
	assert.Equal(t, process.Created, p.State())
}

func TestPMCProcess_Profile(t *testing.T) {
	prof := &ptpv1.PtpProfile{}
	p := NewPMCProcess(0, nil, event.OC, prof, -1)
	assert.Equal(t, prof, p.Profile())
}
