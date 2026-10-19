package cep

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"github.com/google/uuid"
	"github.com/redhat-cne/sdk-go/pkg/event"

	"github.com/golang/glog"
)

// Subscription represents a consumer's interest in events from a specific resource.
type Subscription struct {
	ID       string
	Resource string
	Endpoint string
	W        io.Writer
	active   bool
}

type subscriptionRecord struct {
	ID       string `json:"id"`
	Resource string `json:"resource"`
	Endpoint string `json:"endpoint,omitempty"`
}

// PubSub is a datastore for Subscription objects providing O(1) lookup for
type PubSub struct {
	mu sync.RWMutex
	// subs is the current list of subscriptions. This is used in conjunction with free, which marks indexes which are
	// available. This method of doing this was chosen over a map[uuid]*Subscription in order to keep the subscriptions
	// together in memory for better cache performance. The cost of this is the slight increase in complexity of the
	// code, as well as O(n) lookup of subscriptions by UUID. Given that subscription adds/removes should be infrequent
	// this is not really important.
	subs []Subscription
	// free indicates free spots within the subs slice.
	free []int
	// resourceIndex maps the watched resource to subscriptions, allowing for O(1) lookup
	resourceIndex map[string][]int
	// writerFunc creates an io.Writer for a given endpoint. Used when Subscribe() is called, or when
	// SubscriptionRecords are loaded from disk.
	writerFunc    WriterFunc
	recordStorage Store
}

// NewPubSub creates a PubSub with the given persistent Store and writer factory. ctx bounds the
// lifetime of persistence operations against the Store.
func NewPubSub(storageBackend Store, writerFunc WriterFunc) *PubSub {
	return &PubSub{
		resourceIndex: make(map[string][]int),
		recordStorage: storageBackend,
		writerFunc:    writerFunc,
	}
}

// Subscribe registers a new subscription for the given resource and returns its ID.
func (ps *PubSub) Subscribe(resource, endpoint string) string {
	id := uuid.New().String()
	var w io.Writer
	if endpoint != "" && ps.writerFunc != nil {
		w = ps.writerFunc(endpoint)
	}
	sub := Subscription{ID: id, Resource: resource, Endpoint: endpoint, W: w, active: true}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	var idx int
	if len(ps.free) > 0 {
		idx = ps.free[len(ps.free)-1]
		ps.free = ps.free[:len(ps.free)-1]
		ps.subs[idx] = sub
	} else {
		idx = len(ps.subs)
		ps.subs = append(ps.subs, sub)
	}
	ps.resourceIndex[resource] = append(ps.resourceIndex[resource], idx)

	if err := ps.save(); err != nil {
		glog.Errorf("failed to Save subscriptions: %v", err)
	}
	return id
}

// Unsubscribe removes the subscription with the given ID, returning false if not found.
func (ps *PubSub) Unsubscribe(id string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	idx := -1
	for i, s := range ps.subs {
		if s.active && s.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return false
	}

	resource := ps.subs[idx].Resource
	ps.subs[idx] = Subscription{}
	ps.free = append(ps.free, idx)

	indices := ps.resourceIndex[resource]
	for i, si := range indices {
		if si == idx {
			indices[i] = indices[len(indices)-1]
			indices = indices[:len(indices)-1]
			break
		}
	}
	if len(indices) == 0 {
		delete(ps.resourceIndex, resource)
	} else {
		ps.resourceIndex[resource] = indices
	}

	if err := ps.save(); err != nil {
		glog.Errorf("failed to Save subscriptions: %v", err)
	}
	return true
}

// UnsubscribeAll removes all subscriptions.
func (ps *PubSub) UnsubscribeAll() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.subs = nil
	ps.free = nil
	ps.resourceIndex = make(map[string][]int)
	if err := ps.save(); err != nil {
		glog.Errorf("failed to Save subscriptions: %v", err)
	}
}

type publishTarget struct {
	id string
	w  io.Writer
}

func (ps *PubSub) getTargets(source string) []publishTarget {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	indices := ps.resourceIndex[source]
	targets := make([]publishTarget, 0, len(indices))
	for _, idx := range indices {
		sub := ps.subs[idx]
		if sub.W != nil {
			targets = append(targets, publishTarget{id: sub.ID, w: sub.W})
		}
	}
	return targets
}

// marshalCloudEvent converts an event to its newline-terminated CloudEvent JSON wire form.
func marshalCloudEvent(e *event.Event) ([]byte, error) {
	ce, err := e.NewCloudEventV2()
	if err != nil {
		return nil, fmt.Errorf("failed to convert event to CloudEvent: %w", err)
	}
	data, err := json.Marshal(ce)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal CloudEvent: %w", err)
	}
	return append(data, '\n'), nil
}

// Publish converts the event to a CloudEvent and delivers it to all matching subscribers.
func (ps *PubSub) Publish(e *event.Event) {
	data, err := marshalCloudEvent(e)
	if err != nil {
		glog.Error(err)
		return
	}

	for _, t := range ps.getTargets(e.Source) {
		if _, writeErr := t.w.Write(data); writeErr != nil {
			glog.Errorf("failed to write event to subscriber %s: %v", t.id, writeErr)
		}
	}
}

// PublishToID delivers the event to a single subscription by ID, if it exists and has a writer.
// It is used to send the initial status notification required on subscription creation.
func (ps *PubSub) PublishToID(id string, e *event.Event) {
	data, err := marshalCloudEvent(e)
	if err != nil {
		glog.Error(err)
		return
	}

	ps.mu.RLock()
	var w io.Writer
	for i := range ps.subs {
		if ps.subs[i].active && ps.subs[i].ID == id {
			w = ps.subs[i].W
			break
		}
	}
	ps.mu.RUnlock()

	if w == nil {
		return
	}
	if _, writeErr := w.Write(data); writeErr != nil {
		glog.Errorf("failed to write initial notification to subscriber %s: %v", id, writeErr)
	}
}

// FindByResourceEndpoint returns the ID of an existing active subscription for the given
// resource and endpoint, if one exists. It is used to reject duplicate subscriptions.
func (ps *PubSub) FindByResourceEndpoint(resource, endpoint string) (string, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	for _, idx := range ps.resourceIndex[resource] {
		sub := ps.subs[idx]
		if sub.active && sub.Endpoint == endpoint {
			return sub.ID, true
		}
	}
	return "", false
}

// Get returns the subscription with the given ID.
func (ps *PubSub) Get(id string) (*Subscription, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	for i := range ps.subs {
		if ps.subs[i].active && ps.subs[i].ID == id {
			return &ps.subs[i], true
		}
	}
	return nil, false
}

// List returns all active subscriptions.
func (ps *PubSub) List() []Subscription {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	out := make([]Subscription, 0, len(ps.subs)-len(ps.free))
	for _, s := range ps.subs {
		if s.active {
			out = append(out, s)
		}
	}
	return out
}

// save writes all in-memory subscriptions to ps.storePath. This will overwrite any existing file.
func (ps *PubSub) save() error {
	records := make([]subscriptionRecord, 0, len(ps.subs)-len(ps.free))
	for _, sub := range ps.subs {
		if sub.active {
			records = append(records, subscriptionRecord{ID: sub.ID, Resource: sub.Resource, Endpoint: sub.Endpoint})
		}
	}
	return ps.recordStorage.Save(records)
}

// LoadFromStore replaces the in-memory subscriptions with those persisted to the storage backend
func (ps *PubSub) LoadFromStore(ctx context.Context) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	records, err := ps.recordStorage.Load(ctx)
	if err != nil {
		return fmt.Errorf("error loading subscription records: %w", err)
	}
	ps.subs = make([]Subscription, len(records))
	ps.free = nil
	ps.resourceIndex = make(map[string][]int)
	for i, r := range records {
		var w io.Writer
		if r.Endpoint != "" && ps.writerFunc != nil {
			w = ps.writerFunc(r.Endpoint)
		}
		ps.subs[i] = Subscription{ID: r.ID, Resource: r.Resource, Endpoint: r.Endpoint, W: w, active: true}
		ps.resourceIndex[r.Resource] = append(ps.resourceIndex[r.Resource], i)
	}
	return nil
}
