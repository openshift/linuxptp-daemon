package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

const (
	linkDialTimeout  = 5 * time.Second
	linkWriteTimeout = 5 * time.Second
)

// Link is the bridge between an ipc.Cache and an external program. Link drains a Cache's outbound channel and writes
// each message to a Unix socket as newline-delimited JSON. It also reads from the connection: when it receives a
// status_request, the Link responds with the current state of the cache.
type Link struct {
	socketPath string
	dialFn     func(ctx context.Context) (net.Conn, error)
	cache      *Cache
}

// NewLink creates a Link using the given socket and cache.
func NewLink(socketPath string, cache *Cache) *Link {
	return &Link{
		socketPath: socketPath,
		dialFn: func(ctx context.Context) (net.Conn, error) {
			d := net.Dialer{Timeout: linkDialTimeout}
			return d.DialContext(ctx, "unix", socketPath)
		},
		cache: cache,
	}
}

// Run establishes a connection to the socket, and blocks, waiting on new messages. It will also drain the cache.Out
// channel writing each message to the socket.
func (l *Link) Run(ctx context.Context) {
	for {
		conn := l.dial(ctx)
		if conn == nil {
			return
		}
		inCh := make(chan Message)
		go l.scan(ctx, conn, inCh)

		l.serve(ctx, conn, inCh)
		if err := conn.Close(); err != nil {
			glog.Errorf("Failed to close socket: %v", err)
		}

		if ctx.Err() != nil {
			return
		}
		glog.Info("IPC link: peer disconnected, reconnecting")
	}
}

// dial retries indefinitely with exponential backoff until a connection
// is established or the context is canceled.
func (l *Link) dial(ctx context.Context) net.Conn {
	dialFn := func() (net.Conn, error) { return l.dialFn(ctx) }

	for {
		conn, err := utils.ReconnectWithBackoff(ctx, dialFn, utils.DefaultReconnectConfig())
		if conn != nil {
			return conn
		}
		select {
		case <-ctx.Done():
			return nil
		default:
			glog.Warningf("IPC link: failed to connect to socket: %v, retrying", err)
		}
	}
}

// scan reads incoming messages from r and sends them on ch.
// It closes ch when the scanner fails, signaling that the connection
// is broken.
func (l *Link) scan(ctx context.Context, r io.Reader, ch chan<- Message) {
	defer close(ch)

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		var msg Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			glog.Warningf("IPC link: failed to unmarshal incoming message: %v", err)
			continue
		}
		select {
		case ch <- msg:
		case <-ctx.Done():
			return
		}
	}
}

// serve writes data to the socket as required
// TODO: Edge cases are blurry here, not sure what will happen on failure
func (l *Link) serve(ctx context.Context, conn net.Conn, statusCh <-chan Message) {
	for {
		select {
		case <-ctx.Done():
			return
		// handle status requests
		case msg, ok := <-statusCh:
			if !ok {
				// when scan() closes the chan this will return
				return
			}
			if msg.Type == TypeStatusRequest {
				glog.V(11).Info("IPC link: received status_request, sending snapshot")
				if err := l.writeSnapshot(conn); err != nil {
					glog.Errorf("IPC link: snapshot response failed: %v", err)

					return
				}
			}
		// new cache entry needs to be pushed out
		case msg := <-l.cache.Out():
			if err := l.transmit(conn, msg); err != nil {
				return
			}
		}
	}
}

// writeSnapshot will generate individual messages based on every entry in the cache, and send them to the socket.
func (l *Link) writeSnapshot(conn net.Conn) error {
	msgs := l.cache.Snapshot()
	if len(msgs) == 0 {
		return nil
	}
	for _, msg := range msgs {
		if err := l.transmit(conn, msg); err != nil {
			return fmt.Errorf("failed to encode message: %w", err)
		}
	}
	return nil
}

func (l *Link) transmit(conn net.Conn, msg Message) error {
	if err := conn.SetWriteDeadline(time.Now().Add(linkWriteTimeout)); err != nil {
		glog.Warningf("IPC link: failed to set write deadline: %v", err)
	}
	return Transmit(conn, msg)
}
