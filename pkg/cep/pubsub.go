package cep

import (
	"encoding/json"
	"io"
	"os"
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
	// storePath is the filepath for persistent storage of subscriptionRecords
	storePath string
	// writerFunc creates an io.Writer for a given endpoint. Used when Subscribe() is called, or when
	// SubscriptionRecords are loaded from disk.
	writerFunc WriterFunc
}

// NewPubSub creates a PubSub with the given persistent store path and writer factory.
func NewPubSub(storePath string, writerFunc WriterFunc) *PubSub {
	return &PubSub{
		resourceIndex: make(map[string][]int),
		storePath:     storePath,
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
		glog.Errorf("failed to save subscriptions: %v", err)
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
		glog.Errorf("failed to save subscriptions: %v", err)
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
		glog.Errorf("failed to save subscriptions: %v", err)
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

// Publish converts the event to a CloudEvent and delivers it to all matching subscribers.
func (ps *PubSub) Publish(e *event.Event) {
	ce, err := e.NewCloudEventV2()
	if err != nil {
		glog.Errorf("failed to convert event to CloudEvent: %v", err)
		return
	}
	data, err := json.Marshal(ce)
	if err != nil {
		glog.Errorf("failed to marshal CloudEvent: %v", err)
		return
	}
	data = append(data, '\n')

	for _, t := range ps.getTargets(e.Source) {
		if _, writeErr := t.w.Write(data); writeErr != nil {
			glog.Errorf("failed to write event to subscriber %s: %v", t.id, writeErr)
		}
	}
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
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file and rename to avoid corrupting subscriptions.json on a crash mid-write.
	tmpPath := ps.storePath + ".tmp"
	if writeErr := os.WriteFile(tmpPath, data, 0600); writeErr != nil {
		return writeErr
	}
	return os.Rename(tmpPath, ps.storePath)
}

// LoadFromDisk replaces the in-memory subscriptions with those persisted to ps.storePath
func (ps *PubSub) LoadFromDisk() error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	data, err := os.ReadFile(ps.storePath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var records []subscriptionRecord
	if unmarshalErr := json.Unmarshal(data, &records); unmarshalErr != nil {
		return unmarshalErr
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
