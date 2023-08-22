package event

import (
	"fmt"
	"sync"
)

type Subscriber interface {
	Notify(source EventSource, state PTPState)
	Topic() EventSource
	Monitor()
	MonitoringStarted() bool
	ID() string
}

type Notifier interface {
	Register(o *Subscriber)
	Unregister(o *Subscriber)
}

type StateNotifier struct {
	sync.Mutex
	Subscribers map[string]Subscriber
}
type ReadyNotifier struct {
	sync.Mutex
	Monitors map[string]Subscriber
}

func (n *StateNotifier) Register(s Subscriber) {
	id := fmt.Sprintf("%s_%s", s.Topic(), s.ID())
	n.Lock()
	defer n.Unlock()
	n.Subscribers[id] = s
}

func (n *StateNotifier) Unregister(s Subscriber) {
	id := fmt.Sprintf("%s_%s", s.Topic(), s.ID())
	n.Lock()
	defer n.Unlock()
	if _, ok := n.Subscribers[id]; ok {
		delete(n.Subscribers, id)
	}
}

func (n *StateNotifier) monitor() {
	n.Lock()
	defer n.Unlock()
	for _, o := range n.Subscribers {
		if !o.MonitoringStarted() && o.Topic() == NIL {
			o.Monitor()
		}
	}
}

func (n *StateNotifier) notify(source EventSource, state PTPState) {
	n.Lock()
	defer n.Unlock()
	for _, o := range n.Subscribers {
		if o.Topic() == source && o.Topic() != NIL {
			go o.Notify(source, state)
		}
	}
}

func NewStateNotifier() *StateNotifier {
	return &StateNotifier{
		Subscribers: make(map[string]Subscriber),
	}

}
