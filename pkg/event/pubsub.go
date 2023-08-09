package event

import "sync"

type Subscriber interface {
	Notify(source EventSource, state PTPState)
	Topic() EventSource
}

type Notifier interface {
	Register(o *Subscriber)
	Unregister(o *Subscriber)
	Notify(state PTPState)
}

type StateNotifier struct {
	sync.Mutex
	Subscribers map[Subscriber]struct {
	}
}

func (n *StateNotifier) Register(s Subscriber) {
	n.Subscribers[s] = struct{}{}
}
func (n *StateNotifier) Unregister(s Subscriber) {
	delete(n.Subscribers, s)
}
func (n *StateNotifier) notify(source EventSource, state PTPState) {
	n.Lock()
	defer n.Unlock()
	for o := range n.Subscribers {
		if o.Topic() == source {
			go o.Notify(source, state)
		}
	}
}

func NewStateNotifier() *StateNotifier {
	return &StateNotifier{
		Subscribers: make(map[Subscriber]struct{}),
	}

}
