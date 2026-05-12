package cep

import (
	"path"
	"sync"

	"github.com/google/uuid"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/redhat-cne/sdk-go/pkg/event"
	"github.com/redhat-cne/sdk-go/pkg/event/ptp"
	"github.com/redhat-cne/sdk-go/pkg/types"
)

// EventCache stores the latest cloud event for each IPC message type.
type EventCache struct {
	mu       sync.RWMutex
	nodeName string
	entries  map[string]*event.Event
}

// NewEventCache creates an EventCache for the given node.
func NewEventCache(nodeName string) *EventCache {
	return &EventCache{
		nodeName: nodeName,
		entries:  make(map[string]*event.Event),
	}
}

// Update modifies the cache based on the contents of the given ipc.Message and returns a copy of the resultant
// event.Event. Will return nil in case of a duplicate event.
func (c *EventCache) Update(msg ipc.Message) *event.Event {
	oranType, oranSource, ok := oranMapping(msg.Type)
	if !ok {
		return nil
	}

	resourceAddr := c.buildResourceAddress(msg.Type, msg.IFace)
	dvs := buildDataValues(resourceAddr, msg.Values)
	if len(dvs) == 0 {
		return nil
	}
	contentType := "application/json"

	c.mu.Lock()
	defer c.mu.Unlock()

	e, exists := c.entries[msg.Type]
	if !exists {
		e = &event.Event{
			Type:            string(oranType),
			Source:          path.Join("/cluster/node", c.nodeName, string(oranSource)),
			DataContentType: &contentType,
			Data:            &event.Data{Version: event.APISchemaVersion},
		}
		c.entries[msg.Type] = e
	}

	var changed bool
	if msg.IFace == "" {
		if !exists || !dataValuesEqual(e.Data.Values, dvs) {
			e.Data.Values = dvs
			changed = true
		}
	} else {
		for _, dv := range dvs {
			if setDataValue(e, dv) {
				changed = true
			}
		}
	}

	if !changed {
		return nil
	}

	e.ID = uuid.New().String()
	e.Time, _ = types.ParseTimestamp(msg.Timestamp)

	copiedEvent := copyEvent(e)
	return &copiedEvent
}

// Get returns a copy of the cached event for the given IPC type.
func (c *EventCache) Get(ipcType string) (event.Event, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.entries[ipcType]
	if !ok {
		return event.Event{}, false
	}
	return copyEvent(e), true
}

// GetBySource returns a copy of the cached event matching the given source path.
func (c *EventCache) GetBySource(source string) (event.Event, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, e := range c.entries {
		if e.Source == source {
			return copyEvent(e), true
		}
	}
	return event.Event{}, false
}

// Clear deletes all entries in the EventCache
func (c *EventCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*event.Event)
}

func dataValuesEqual(a, b []event.DataValue) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Resource != b[i].Resource || a[i].DataType != b[i].DataType || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

func (c *EventCache) buildResourceAddress(ipcType string, iface string) string {
	prefix := path.Join("/cluster/node", c.nodeName)

	switch ipcType {
	case ipc.TypePTPState, ipc.TypeGNSSState:
		return path.Join(prefix, iface, "master")
	case ipc.TypeClockClass:
		return path.Join(prefix, iface, "master", "clock-class")
	case ipc.TypeOSClockState:
		return path.Join(prefix, "CLOCK_REALTIME")
	case ipc.TypeSyncEState, ipc.TypeSyncEClockQuality:
		return path.Join(prefix, iface)
	case ipc.TypeSyncState:
		return path.Join(prefix, string(ptp.SyncStatusState))
	default:
		return prefix
	}
}
