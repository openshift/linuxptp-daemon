package daemon

import (
	"io"
	"os"
	"os/exec"
)

// ProcessCmd is the interface used by run()/Stop() on types that exec a child.
// Production uses ExecCmd (wraps *exec.Cmd). Tests use MockCmd.
type ProcessCmd interface {
	StdoutPipe() (io.ReadCloser, error)
	SetStderr(w io.Writer)
	RedirectStderrToStdout()
	Start() error
	Wait() error
	Signal(sig os.Signal) error
	Pid() int
	Clone() ProcessCmd
}

// ExecCmd wraps *exec.Cmd to satisfy ProcessCmd.
type ExecCmd struct {
	cmd *exec.Cmd
}

// NewExecCmd wraps an *exec.Cmd for use as a ProcessCmd.
func NewExecCmd(cmd *exec.Cmd) *ExecCmd {
	return &ExecCmd{cmd: cmd}
}

// StdoutPipe returns a pipe connected to the command's standard output.
func (e *ExecCmd) StdoutPipe() (io.ReadCloser, error) { return e.cmd.StdoutPipe() }

// SetStderr sets the command's standard error output.
func (e *ExecCmd) SetStderr(w io.Writer) { e.cmd.Stderr = w }

// RedirectStderrToStdout redirects the command's standard error to its standard output.
func (e *ExecCmd) RedirectStderrToStdout() { e.cmd.Stderr = e.cmd.Stdout }

// Start starts the command.
func (e *ExecCmd) Start() error { return e.cmd.Start() }

// Wait waits for the command to exit.
func (e *ExecCmd) Wait() error { return e.cmd.Wait() }

// Pid returns the process ID of the command.
func (e *ExecCmd) Pid() int { return e.cmd.Process.Pid }

// Signal sends a signal to the running command process.
func (e *ExecCmd) Signal(sig os.Signal) error { return e.cmd.Process.Signal(sig) }

// Clone creates a copy of the ExecCmd with the same command.
func (e *ExecCmd) Clone() ProcessCmd {
	if e.cmd == nil || len(e.cmd.Args) == 0 {
		return e
	}
	return NewExecCmd(exec.Command(e.cmd.Args[0], e.cmd.Args[1:]...))
}
