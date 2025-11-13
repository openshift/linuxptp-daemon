package utils

import (
	"syscall"
	"time"

	expect "github.com/google/goexpect"
)

const (
	sigTimeout = 500 * time.Millisecond
)

// CloseExpect gracefully closes a GExpect instance by sending SIGTERM and waiting for completion.
func CloseExpect(exp *expect.GExpect, r <-chan error) {
	exp.SendSignal(syscall.SIGTERM)
	for timeout := time.After(sigTimeout); ; {
		select {
		case <-r:
			exp.Close()
			return
		case <-timeout:
			exp.Send("\x03")
			exp.Close()
			return
		}
	}
}
