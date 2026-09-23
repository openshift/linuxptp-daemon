package ipc

import (
	"sync"
	"time"

	"github.com/golang/glog"
)

type cacheKey struct {
	msgType string
	profile string
	iface   string
}

// Cache tracks the latest IPC message per (type, profile, iface) and provides a channel for outbound messages. It
// deduplicates by suppressing messages whose values match the last-emitted state for the same key.
type Cache struct {
	mu    sync.Mutex
	store map[cacheKey]Message
	outCh chan Message
}

// NewCache creates a cache with a buffered outbound channel of the given size.
func NewCache(bufSize int) *Cache {
	return &Cache{
		store: make(map[cacheKey]Message),
		outCh: make(chan Message, bufSize),
	}
}

// Send stores a message in the cache and writes it to the outbound channel. Returns false if the message is a duplicate
// (same Values and IFace as the last message for this type/profile key).
func (c *Cache) Send(msg Message) bool {
	if msg.Version == 0 {
		msg.Version = Version
	}
	if msg.Timestamp == "" {
		msg.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey{msgType: msg.Type, profile: msg.Profile, iface: msg.IFace}
	if prev, ok := c.store[key]; ok && prev.ValuesEqual(msg) {
		return false
	}
	c.store[key] = msg
	glog.V(14).Infof("Sending IPC message type=%s iface=%s value %v", msg.Type, msg.IFace, msg.Values)

	// In case of a full buffer, drop the message. The state will be recorded within the cache, but not sent
	// This should only realistically happen if cloud-event-proxy goes down for some reason. In that case, when it comes
	// back up it will request a snapshot, and will receive the missed event.
	select {
	case c.outCh <- msg:
	default:
		glog.V(14).Info("channel full, dropping message")
		// drop message
	}
	return true
}

// Snapshot returns a copy of all cached messages.
func (c *Cache) Snapshot() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	msgs := make([]Message, 0, len(c.store))
	for _, msg := range c.store {
		msgs = append(msgs, msg)
	}
	return msgs
}

// Clear removes all cached messages and sends a cache_clear message to notify
// the peer that all prior state is invalidated.
func (c *Cache) Clear() {
	c.mu.Lock()
	c.store = make(map[cacheKey]Message)
	c.mu.Unlock()
	select {
	case c.outCh <- Message{Version: Version, Type: TypeCacheClear}:
	default:
	}
}

// Out returns the read-only outbound message channel.
func (c *Cache) Out() <-chan Message {
	return c.outCh
}
