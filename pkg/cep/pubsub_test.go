package cep

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/redhat-cne/sdk-go/pkg/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSub1 = "sub-1"
	testSub2 = "sub-2"
)

func makeTestEvent(source string) *event.Event {
	ct := "application/json"
	return &event.Event{
		ID:              "test-id",
		Type:            "test-type",
		Source:          source,
		DataContentType: &ct,
		Data:            &event.Data{Version: "v1"},
	}
}

func testPubSub(t *testing.T, writers map[string]*bytes.Buffer) *PubSub {
	t.Helper()
	return NewPubSub(filepath.Join(t.TempDir(), "subscriptions.json"), func(endpoint string) io.Writer {
		buf := &bytes.Buffer{}
		if writers != nil {
			writers[endpoint] = buf
		}
		return buf
	})
}

func TestSubscribe(t *testing.T) {
	t.Run("single subscription", func(t *testing.T) {
		ps := testPubSub(t, nil)
		ps.Subscribe("/resource/a", "ep-1")

		assert.Len(t, ps.List(), 1)
		assert.Len(t, ps.subs, 1)
		assert.Empty(t, ps.free)
	})

	t.Run("two subscriptions grow the arena", func(t *testing.T) {
		ps := testPubSub(t, nil)
		ps.Subscribe("/resource/a", "ep-1")
		ps.Subscribe("/resource/b", "ep-2")

		assert.Len(t, ps.List(), 2)
		assert.Len(t, ps.subs, 2)
		assert.Empty(t, ps.free)
	})

	t.Run("unsubscribe adds to free list", func(t *testing.T) {
		ps := testPubSub(t, nil)
		id := ps.Subscribe("/resource/a", "ep-1")
		ps.Unsubscribe(id)

		assert.Empty(t, ps.List())
		assert.Len(t, ps.subs, 1, "backing slice keeps the slot")
		assert.Len(t, ps.free, 1)
	})

	t.Run("resubscribe reuses freed slot", func(t *testing.T) {
		ps := testPubSub(t, nil)
		id := ps.Subscribe("/resource/a", "ep-1")
		ps.Unsubscribe(id)
		ps.Subscribe("/resource/b", "ep-2")

		assert.Len(t, ps.List(), 1)
		assert.Len(t, ps.subs, 1, "arena did not grow")
		assert.Empty(t, ps.free)
	})

	t.Run("reuse middle slot", func(t *testing.T) {
		ps := testPubSub(t, nil)
		ps.Subscribe("/resource/a", "ep-1")
		id2 := ps.Subscribe("/resource/b", "ep-2")
		ps.Subscribe("/resource/c", "ep-3")
		ps.Unsubscribe(id2)
		ps.Subscribe("/resource/d", "ep-4")

		assert.Len(t, ps.List(), 3)
		assert.Len(t, ps.subs, 3, "arena did not grow")
		assert.Empty(t, ps.free)
	})

	t.Run("empty endpoint skips writerFunc", func(t *testing.T) {
		var writerCalls int
		ps := NewPubSub(filepath.Join(t.TempDir(), "subscriptions.json"), func(_ string) io.Writer {
			writerCalls++
			return &bytes.Buffer{}
		})
		ps.Subscribe("/resource/a", "")

		assert.Zero(t, writerCalls)
		assert.Nil(t, ps.List()[0].W)
	})
}

func TestPublish(t *testing.T) {
	type sub struct {
		resource string
		endpoint string
	}

	tests := []struct {
		name          string
		subs          []sub
		publishSource string
		wantDelivered []string
		wantEmpty     []string
	}{
		{
			name:          "matching resource delivers event",
			subs:          []sub{{testPTPLockState, testSub1}},
			publishSource: testPTPLockState,
			wantDelivered: []string{testSub1},
		},
		{
			name:          "non-matching resource gets nothing",
			subs:          []sub{{testPTPLockState, testSub1}},
			publishSource: "/cluster/node/worker-0/sync/gnss-status/gnss-sync-status",
			wantEmpty:     []string{testSub1},
		},
		{
			name: "multiple subscribers on same resource all receive",
			subs: []sub{
				{testPTPLockState, testSub1},
				{testPTPLockState, testSub2},
			},
			publishSource: testPTPLockState,
			wantDelivered: []string{testSub1, testSub2},
		},
		{
			name: "only matching subscriber receives",
			subs: []sub{
				{testPTPLockState, testSub1},
				{"/cluster/node/worker-0/sync/gnss-status/gnss-sync-status", testSub2},
			},
			publishSource: testPTPLockState,
			wantDelivered: []string{testSub1},
			wantEmpty:     []string{testSub2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writers := map[string]*bytes.Buffer{}
			ps := testPubSub(t, writers)

			for _, s := range tt.subs {
				ps.Subscribe(s.resource, s.endpoint)
			}

			ps.Publish(makeTestEvent(tt.publishSource))

			for _, ep := range tt.wantDelivered {
				buf := writers[ep]
				require.NotEmpty(t, buf.Bytes(), "endpoint %s should have received an event", ep)
				var got ce.Event
				require.NoError(t, json.NewDecoder(buf).Decode(&got), "endpoint %s", ep)
				assert.Equal(t, tt.publishSource, got.Source(), "endpoint %s source mismatch", ep)
			}
			for _, ep := range tt.wantEmpty {
				assert.Empty(t, writers[ep].Bytes(), "endpoint %s should not have received an event", ep)
			}
		})
	}
}

func TestUnsubscribe(t *testing.T) {
	type sub struct {
		resource string
		endpoint string
	}

	tests := []struct {
		name             string
		subs             []sub
		unsubIndices     []int
		unsubAll         bool
		unsubNonexistent bool
		wantListLen      int
		wantResources    []string
		publishSource    string
		wantDelivered    []string
		wantEmpty        []string
	}{
		{
			name:          "no delivery after unsubscribe",
			subs:          []sub{{testPTPLockState, testSub1}},
			unsubIndices:  []int{0},
			wantListLen:   0,
			publishSource: testPTPLockState,
			wantEmpty:     []string{testSub1},
		},
		{
			name:             "nonexistent returns false",
			unsubNonexistent: true,
		},
		{
			name:         "removes from subs and index",
			subs:         []sub{{testPTPLockState, testSub1}},
			unsubIndices: []int{0},
			wantListLen:  0,
		},
		{
			name: "unsubscribe one of two on same resource",
			subs: []sub{
				{testPTPLockState, testSub1},
				{testPTPLockState, testSub2},
			},
			unsubIndices:  []int{0},
			wantListLen:   1,
			wantResources: []string{testPTPLockState},
			publishSource: testPTPLockState,
			wantDelivered: []string{testSub2},
			wantEmpty:     []string{testSub1},
		},
		{
			name: "unsubscribe all",
			subs: []sub{
				{"/resource/a", "sub-a"},
				{"/resource/b", "sub-b"},
			},
			unsubAll:    true,
			wantListLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writers := map[string]*bytes.Buffer{}
			ps := testPubSub(t, writers)

			ids := make([]string, len(tt.subs))
			for i, s := range tt.subs {
				ids[i] = ps.Subscribe(s.resource, s.endpoint)
			}

			if tt.unsubNonexistent {
				assert.False(t, ps.Unsubscribe("nonexistent"))
				return
			}

			if tt.unsubAll {
				ps.UnsubscribeAll()
			} else {
				for _, idx := range tt.unsubIndices {
					assert.True(t, ps.Unsubscribe(ids[idx]))
				}
			}

			assert.Len(t, ps.List(), tt.wantListLen)
			for _, r := range tt.wantResources {
				assert.NotEmpty(t, ps.resourceIndex[r], "resourceIndex should have entries for %s", r)
			}
			if tt.wantListLen == 0 && len(tt.wantResources) == 0 {
				assert.Empty(t, ps.resourceIndex)
			}

			if tt.publishSource != "" {
				ps.Publish(makeTestEvent(tt.publishSource))
				for _, ep := range tt.wantDelivered {
					require.NotEmpty(t, writers[ep].Bytes(), "endpoint %s should have received an event", ep)
				}
				for _, ep := range tt.wantEmpty {
					assert.Empty(t, writers[ep].Bytes(), "endpoint %s should not have received an event", ep)
				}
			}
		})
	}
}

func TestGet(t *testing.T) {
	tests := []struct {
		name         string
		subResource  string
		lookupID     string
		useRealID    bool
		wantFound    bool
		wantResource string
	}{
		{
			name:         "found",
			subResource:  testPTPLockState,
			useRealID:    true,
			wantFound:    true,
			wantResource: testPTPLockState,
		},
		{
			name:      "not found",
			lookupID:  "nonexistent",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ps := testPubSub(t, nil)

			var realID string
			if tt.subResource != "" {
				realID = ps.Subscribe(tt.subResource, "sub")
			}

			lookupID := tt.lookupID
			if tt.useRealID {
				lookupID = realID
			}

			sub, ok := ps.Get(lookupID)
			assert.Equal(t, tt.wantFound, ok)
			if tt.wantFound {
				assert.Equal(t, tt.wantResource, sub.Resource)
			}
		})
	}
}

func TestPubSubPersistence(t *testing.T) {
	type sub struct {
		resource string
		endpoint string
	}

	tests := []struct {
		name          string
		subs          []sub
		unsubIndices  []int
		wantListLen   int
		wantResources []string
		wantWriterNil bool
	}{
		{
			name:          "survives reload",
			subs:          []sub{{testPTPLockState, ""}},
			wantListLen:   1,
			wantResources: []string{testPTPLockState},
			wantWriterNil: true,
		},
		{
			name:         "empty after unsubscribe",
			subs:         []sub{{testPTPLockState, ""}},
			unsubIndices: []int{0},
			wantListLen:  0,
		},
		{
			name:          "writerFunc called on load",
			subs:          []sub{{testPTPLockState, "http://example.com/events"}},
			wantListLen:   1,
			wantResources: []string{testPTPLockState},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storePath := filepath.Join(t.TempDir(), "subscriptions.json")

			ps1 := NewPubSub(storePath, nil)
			ids := make([]string, len(tt.subs))
			for i, s := range tt.subs {
				ids[i] = ps1.Subscribe(s.resource, s.endpoint)
			}
			for _, idx := range tt.unsubIndices {
				ps1.Unsubscribe(ids[idx])
			}

			var writerCalled bool
			ps2 := NewPubSub(storePath, func(_ string) io.Writer {
				writerCalled = true
				return &bytes.Buffer{}
			})
			require.NoError(t, ps2.LoadFromDisk())

			assert.Len(t, ps2.List(), tt.wantListLen)
			for _, r := range tt.wantResources {
				assert.NotEmpty(t, ps2.resourceIndex[r], "resourceIndex should have entries for %s", r)
			}
			if tt.wantListLen == 0 {
				assert.Empty(t, ps2.resourceIndex)
			}

			for _, sub := range ps2.List() {
				if tt.wantWriterNil {
					assert.Nil(t, sub.W)
				} else {
					assert.True(t, writerCalled, "writerFunc should have been called on load")
				}
			}
		})
	}
}
