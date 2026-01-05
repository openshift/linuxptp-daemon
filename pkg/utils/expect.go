// Package utils provides utility functions for the linuxptp daemon.
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
	if exp == nil {
		return
	}
	defer func() {
		_ = exp.Close()
	}()
	_ = exp.SendSignal(syscall.SIGTERM)
	for timeout := time.After(sigTimeout); ; {
		select {
		case <-r:
			return
		case <-timeout:
			_ = exp.Send("\x03")
			return
		}
	}
}
