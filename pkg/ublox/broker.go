package ublox

import (
	"context"
	"sync"
	"sync/atomic"
)

const subscriptionBufferSize = 32

type messageBroker struct {
	mu            sync.RWMutex
	nextID        atomic.Uint64
	subscriptions map[uint64]*messageSubscription
}

type messageSubscription struct {
	id    uint64
	types map[MessageType]struct{}
	ch    chan Message
	done  chan struct{}

	mu     sync.RWMutex
	closed bool
}

// newMessageBroker creates an empty broker with no subscriptions.
func newMessageBroker() *messageBroker {
	return &messageBroker{subscriptions: make(map[uint64]*messageSubscription)}
}

// Subscribe registers a fan-out subscription. If no message types are
// supplied, the subscription receives all messages.
func (b *messageBroker) Subscribe(ctx context.Context, types ...MessageType) *Subscription {
	wanted := make(map[MessageType]struct{}, len(types))
	for _, typ := range types {
		wanted[typ] = struct{}{}
	}

	id := b.nextID.Add(1)
	sub := &messageSubscription{
		id:    id,
		types: wanted,
		ch:    make(chan Message, subscriptionBufferSize),
		done:  make(chan struct{}),
	}

	b.mu.Lock()
	b.subscriptions[id] = sub
	b.mu.Unlock()

	result := &Subscription{
		Messages: sub.ch,
		cancel:   func() { b.unsubscribe(sub) },
	}
	go func() {
		select {
		case <-ctx.Done():
			result.Cancel()
		case <-sub.done:
		}
	}()
	return result
}

// unsubscribe removes a subscription and closes its channels.
func (b *messageBroker) unsubscribe(sub *messageSubscription) {
	b.mu.Lock()
	delete(b.subscriptions, sub.id)
	b.mu.Unlock()

	sub.mu.Lock()
	if !sub.closed {
		sub.closed = true
		close(sub.done)
		close(sub.ch)
	}
	sub.mu.Unlock()
}

// Publish delivers a message to every matching subscription.
func (b *messageBroker) Publish(message Message) {
	b.mu.RLock()
	subs := make([]*messageSubscription, 0, len(b.subscriptions))
	for _, sub := range b.subscriptions {
		_, typeMatch := sub.types[message.Type]
		if len(sub.types) == 0 || typeMatch {
			subs = append(subs, sub)
		}
	}
	b.mu.RUnlock()

	for _, sub := range subs {
		sub.deliver(message)
	}
}

// deliver queues a message while retaining the newest value for slow consumers.
func (s *messageSubscription) deliver(message Message) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return
	}

	// The reader must not be stopped by a slow telemetry consumer. Retain the
	// newest message when a subscription's bounded queue is full.
	select {
	case s.ch <- message:
	default:
		select {
		case <-s.ch:
		default:
		}
		select {
		case s.ch <- message:
		default:
		}
	}
}
