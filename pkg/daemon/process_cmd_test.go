package daemon

import (
	"fmt"
	"io"
	"os"
)

// MockCmd implements ProcessCmd for tests. Send lines to it via SendLine.
// It blocks on Wait() until Signal() is called (simulating SIGTERM).
type MockCmd struct {
	pw     *io.PipeWriter
	pr     *io.PipeReader
	waitCh chan struct{}
	pid    int
}

// NewMockCmd creates a MockCmd ready for use.
func NewMockCmd() *MockCmd {
	pr, pw := io.Pipe()
	return &MockCmd{
		pw:     pw,
		pr:     pr,
		waitCh: make(chan struct{}),
		pid:    99999,
	}
}

// SendLine writes one line to the mock process's stdout.
// The scanner in runScanner splits on newlines, so each
// SendLine call produces exactly one scanner.Text() value.
func (m *MockCmd) SendLine(line string) {
	fmt.Fprintln(m.pw, line)
}

func (m *MockCmd) StdoutPipe() (io.ReadCloser, error) {
	return m.pr, nil
}

func (m *MockCmd) SetStderr(_ io.Writer)   {}
func (m *MockCmd) RedirectStderrToStdout() {}
func (m *MockCmd) Start() error            { return nil }

func (m *MockCmd) Wait() error {
	<-m.waitCh
	return nil
}

// Signal closes the pipe and unblocks Wait, like SIGTERM killing a real process.
func (m *MockCmd) Signal(_ os.Signal) error {
	m.pw.Close()
	select {
	case <-m.waitCh:
	default:
		close(m.waitCh)
	}
	return nil
}

func (m *MockCmd) Pid() int          { return m.pid }
func (m *MockCmd) Clone() ProcessCmd { return NewMockCmd() }

// DeadMockCmd is a MockCmd variant that exits immediately on Wait,
// simulating a process that crashes right after Start.
type DeadMockCmd struct {
	MockCmd
}

func NewDeadMockCmd() *DeadMockCmd {
	m := NewMockCmd()
	m.pw.Close()
	close(m.waitCh)
	return &DeadMockCmd{MockCmd: *m}
}

func (d *DeadMockCmd) Clone() ProcessCmd { return NewDeadMockCmd() }
