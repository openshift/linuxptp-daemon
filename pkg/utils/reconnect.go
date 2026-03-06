// Package utils provides utility functions for the linuxptp daemon.
//
//nolint:revive // utils is a common and acceptable package name
package utils

import (
	"context"
	"net"
	"time"

	"github.com/golang/glog"
)

const (
	// DefaultMaxReconnectAttempts is the default maximum number of reconnection attempts.
	DefaultMaxReconnectAttempts = 10
	// DefaultReconnectBackoffBase is the default initial backoff duration between reconnection attempts.
	DefaultReconnectBackoffBase = 100 * time.Millisecond
	// DefaultMaxReconnectBackoff is the default maximum backoff duration between reconnection attempts.
	DefaultMaxReconnectBackoff = 5 * time.Second
)

// ReconnectConfig holds the parameters for socket reconnection with exponential backoff.
type ReconnectConfig struct {
	// MaxAttempts is the maximum number of reconnection attempts before giving up.
	MaxAttempts int
	// BackoffBase is the initial backoff duration between reconnection attempts.
	BackoffBase time.Duration
	// MaxBackoff is the maximum backoff duration (cap for exponential growth).
	MaxBackoff time.Duration
}

// DefaultReconnectConfig returns a ReconnectConfig with sensible defaults.
func DefaultReconnectConfig() ReconnectConfig {
	return ReconnectConfig{
		MaxAttempts: DefaultMaxReconnectAttempts,
		BackoffBase: DefaultReconnectBackoffBase,
		MaxBackoff:  DefaultMaxReconnectBackoff,
	}
}

// ReconnectWithBackoff attempts to establish a connection using the provided dial function.
// It retries with exponential backoff up to cfg.MaxAttempts times, and is responsive to
// cancellation via the provided context.
// Returns the new connection, or nil if all attempts are exhausted or the context is cancelled.
func ReconnectWithBackoff(ctx context.Context, dialFn func() (net.Conn, error), cfg ReconnectConfig) net.Conn {
	glog.Info("Attempting to reconnect to event socket")

	backoff := cfg.BackoffBase
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			glog.Info("Stop signal received, aborting reconnect attempt")
			return nil
		default:
		}

		newConn, err := dialFn()
		if err == nil {
			glog.Infof("Successfully reconnected to event socket after %d attempt(s)", attempt)
			return newConn
		}

		if attempt < cfg.MaxAttempts {
			glog.Warningf("Failed to reconnect to event socket (attempt %d/%d): %v, retrying in %v",
				attempt, cfg.MaxAttempts, err, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				glog.Info("Stop signal received during backoff, aborting reconnect")
				return nil
			}
			backoff *= 2
			if backoff > cfg.MaxBackoff {
				backoff = cfg.MaxBackoff
			}
		}
	}

	glog.Errorf("Failed to reconnect to event socket after %d attempts", cfg.MaxAttempts)
	return nil
}
