package utils_test

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
)

// --- DefaultReconnectConfig ---

func TestDefaultReconnectConfig(t *testing.T) {
	cfg := utils.DefaultReconnectConfig()
	assert.Equal(t, utils.DefaultMaxReconnectAttempts, cfg.MaxAttempts)
	assert.Equal(t, utils.DefaultReconnectBackoffBase, cfg.BackoffBase)
	assert.Equal(t, utils.DefaultMaxReconnectBackoff, cfg.MaxBackoff)
}

// --- IsBrokenPipe ---

func TestIsBrokenPipe(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"generic error", errors.New("some error"), false},
		{"EPIPE", syscall.EPIPE, true},
		{"ECONNRESET", syscall.ECONNRESET, true},
		{"ECONNREFUSED", syscall.ECONNREFUSED, true},
		{"ENOTCONN", syscall.ENOTCONN, true},
		{"wrapped EPIPE in OpError", &net.OpError{Op: "write", Err: syscall.EPIPE}, true},
		{"wrapped generic in OpError", &net.OpError{Op: "write", Err: errors.New("other")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, utils.IsBrokenPipe(tt.err))
		})
	}
}

// --- ReconnectWithBackoff ---

func TestReconnectWithBackoff_SuccessOnFirstAttempt(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	dialFn := func() (net.Conn, error) {
		return client, nil
	}
	cfg := utils.ReconnectConfig{
		MaxAttempts: 3,
		BackoffBase: 1 * time.Millisecond,
		MaxBackoff:  10 * time.Millisecond,
	}
	conn := utils.ReconnectWithBackoff(context.Background(), dialFn, cfg)
	assert.NotNil(t, conn)
}

func TestReconnectWithBackoff_SuccessAfterRetries(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	attempts := 0
	dialFn := func() (net.Conn, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("connection refused")
		}
		return client, nil
	}
	cfg := utils.ReconnectConfig{
		MaxAttempts: 5,
		BackoffBase: 1 * time.Millisecond,
		MaxBackoff:  5 * time.Millisecond,
	}
	conn := utils.ReconnectWithBackoff(context.Background(), dialFn, cfg)
	assert.NotNil(t, conn)
	assert.Equal(t, 3, attempts)
}

func TestReconnectWithBackoff_ExhaustedRetries(t *testing.T) {
	dialFn := func() (net.Conn, error) {
		return nil, errors.New("connection refused")
	}
	cfg := utils.ReconnectConfig{
		MaxAttempts: 3,
		BackoffBase: 1 * time.Millisecond,
		MaxBackoff:  5 * time.Millisecond,
	}
	conn := utils.ReconnectWithBackoff(context.Background(), dialFn, cfg)
	assert.Nil(t, conn)
}

func TestReconnectWithBackoff_CancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	called := false
	dialFn := func() (net.Conn, error) {
		called = true
		return nil, errors.New("should not be called")
	}
	cfg := utils.ReconnectConfig{
		MaxAttempts: 5,
		BackoffBase: 1 * time.Millisecond,
		MaxBackoff:  5 * time.Millisecond,
	}
	conn := utils.ReconnectWithBackoff(ctx, dialFn, cfg)
	assert.Nil(t, conn)
	assert.False(t, called, "dialFn should not have been called after context was cancelled")
}

func TestReconnectWithBackoff_CancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	attempts := 0
	dialFn := func() (net.Conn, error) {
		attempts++
		if attempts == 1 {
			// Cancel context after first failed attempt; will be detected during backoff
			go func() {
				time.Sleep(1 * time.Millisecond)
				cancel()
			}()
		}
		return nil, errors.New("connection refused")
	}
	cfg := utils.ReconnectConfig{
		MaxAttempts: 10,
		BackoffBase: 100 * time.Millisecond, // long enough for cancel to fire
		MaxBackoff:  1 * time.Second,
	}
	start := time.Now()
	conn := utils.ReconnectWithBackoff(ctx, dialFn, cfg)
	elapsed := time.Since(start)
	assert.Nil(t, conn)
	assert.Equal(t, 1, attempts, "should have stopped after first attempt due to cancellation")
	assert.Less(t, elapsed, 500*time.Millisecond, "should have returned quickly after cancellation")
}

func TestReconnectWithBackoff_BackoffCapsAtMaxBackoff(t *testing.T) {
	attempts := 0
	dialFn := func() (net.Conn, error) {
		attempts++
		return nil, errors.New("connection refused")
	}
	cfg := utils.ReconnectConfig{
		MaxAttempts: 5,
		BackoffBase: 1 * time.Millisecond,
		MaxBackoff:  2 * time.Millisecond, // base=1ms, doubles to 2ms then caps
	}
	start := time.Now()
	conn := utils.ReconnectWithBackoff(context.Background(), dialFn, cfg)
	elapsed := time.Since(start)
	assert.Nil(t, conn)
	assert.Equal(t, 5, attempts)
	// With cap at 2ms and 4 sleeps: 1 + 2 + 2 + 2 = 7ms total sleep (plus overhead)
	assert.Less(t, elapsed, 200*time.Millisecond, "backoff should be capped and fast")
}
