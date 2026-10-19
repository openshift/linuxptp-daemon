package cep

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDiskStore_LoadFreshIsEmpty(t *testing.T) {
	// A freshly created store file is seeded with "[]", so the first Load returns no records (not an
	// "unexpected end of JSON input" error).
	store, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	require.NoError(t, err)

	records, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestDiskStore_LoadZeroByteFile(t *testing.T) {
	// A zero-byte file left by an earlier version must be treated as no records, not an error.
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	require.NoError(t, os.WriteFile(path, nil, 0600))

	store, err := NewDiskStore(path)
	require.NoError(t, err)

	records, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Empty(t, records)
}

const (
	testCMNamespace = "openshift-ptp"
	testCMName      = "worker-0"
)

func TestConfigMapStore_CreatesConfigMap(t *testing.T) {
	client := fake.NewClientset()
	_, err := NewConfigMapStore(context.Background(), client, testCMNamespace, testCMName)
	require.NoError(t, err)

	cm, err := client.CoreV1().ConfigMaps(testCMNamespace).Get(context.Background(), testCMName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "[]", cm.Data[configMapStoreKey])
}

func TestConfigMapStore_ExistingConfigMapNotOverwritten(t *testing.T) {
	// A ConfigMap that already holds records must survive construction (e.g. a pod restart).
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testCMName, Namespace: testCMNamespace},
		Data:       map[string]string{configMapStoreKey: `[{"id":"a","resource":"/r/a","endpoint":"ep-a"}]`},
	}
	client := fake.NewClientset(existing)

	store, err := NewConfigMapStore(context.Background(), client, testCMNamespace, testCMName)
	require.NoError(t, err)

	records, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, subscriptionRecord{ID: "a", Resource: "/r/a", Endpoint: "ep-a"}, records[0])
}

func TestConfigMapStore_SaveLoadRoundTrip(t *testing.T) {
	client := fake.NewClientset()
	store, err := NewConfigMapStore(context.Background(), client, testCMNamespace, testCMName)
	require.NoError(t, err)

	want := []subscriptionRecord{
		{ID: "1", Resource: "/r/a", Endpoint: "ep-1"},
		{ID: "2", Resource: "/r/b"},
	}
	require.NoError(t, store.Save(want))

	got, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestConfigMapStore_LoadEmptyReturnsNil(t *testing.T) {
	client := fake.NewClientset()
	store, err := NewConfigMapStore(context.Background(), client, testCMNamespace, testCMName)
	require.NoError(t, err)

	records, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestConfigMapStore_SaveOverwrites(t *testing.T) {
	client := fake.NewClientset()
	store, err := NewConfigMapStore(context.Background(), client, testCMNamespace, testCMName)
	require.NoError(t, err)

	require.NoError(t, store.Save([]subscriptionRecord{{ID: "1", Resource: "/r/a"}}))
	require.NoError(t, store.Save([]subscriptionRecord{{ID: "2", Resource: "/r/b"}}))

	got, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "2", got[0].ID)
}

func TestConfigMapStore_LoadMissingConfigMap(t *testing.T) {
	// Load must surface a real error if the backing ConfigMap has gone missing.
	client := fake.NewClientset()
	store := &configMapStore{client: client, namespace: testCMNamespace, name: testCMName, key: configMapStoreKey}

	_, err := store.Load(context.Background())
	require.Error(t, err)
	assert.True(t, apierrors.IsNotFound(err), "expected NotFound, got %v", err)
}

// legacyV1ConfigMap builds a ConfigMap in the cloud-event-proxy v1 schema: one key per subscriber
// (keyed by client UUID), each value a serialized subscriber object.
func legacyV1ConfigMap(subscribers map[string]string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: testCMName, Namespace: testCMNamespace},
		Data:       subscribers,
	}
}

func TestConfigMapStore_LoadLegacyV1Schema(t *testing.T) {
	const clientID = "8a1ccedf-1111-2222-3333-444455556666"
	legacy := `{
		"clientID": "` + clientID + `",
		"EndpointUri": "http://consumer.example.com:9043/event",
		"status": 1,
		"subStore": {
			"store": {
				"/cluster/node/worker-0/sync/ptp-status/lock-state": {
					"SubscriptionId": "f3b2aaaa-0000-1111-2222-333344445555",
					"EndpointUri": "http://consumer.example.com:9043/event",
					"UriLocation": "http://localhost:9043/api/ocloudNotifications/v2/subscriptions/f3b2aaaa-0000-1111-2222-333344445555",
					"ResourceAddress": "/cluster/node/worker-0/sync/ptp-status/lock-state"
				}
			}
		}
	}`
	client := fake.NewClientset(legacyV1ConfigMap(map[string]string{clientID: legacy}))
	store, err := NewConfigMapStore(context.Background(), client, testCMNamespace, testCMName)
	require.NoError(t, err)

	records, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, subscriptionRecord{
		ID:       "f3b2aaaa-0000-1111-2222-333344445555",
		Resource: "/cluster/node/worker-0/sync/ptp-status/lock-state",
		Endpoint: "http://consumer.example.com:9043/event",
	}, records[0])
}

func TestConfigMapStore_LoadLegacyV1MultipleResources(t *testing.T) {
	// A single legacy subscriber can hold many resources in its subStore; each becomes one record.
	const clientID = "8a1ccedf-1111-2222-3333-444455556666"
	legacy := `{
		"clientID": "` + clientID + `",
		"EndpointUri": "http://fallback.example.com:9043/event",
		"subStore": {
			"store": {
				"/cluster/node/worker-0/sync/ptp-status/lock-state": {
					"SubscriptionId": "id-lock",
					"EndpointUri": "http://consumer.example.com:9043/event",
					"ResourceAddress": "/cluster/node/worker-0/sync/ptp-status/lock-state"
				},
				"/cluster/node/worker-0/sync/sync-status/clock-class": {
					"ResourceAddress": "/cluster/node/worker-0/sync/sync-status/clock-class"
				}
			}
		}
	}`
	client := fake.NewClientset(legacyV1ConfigMap(map[string]string{clientID: legacy}))
	store, err := NewConfigMapStore(context.Background(), client, testCMNamespace, testCMName)
	require.NoError(t, err)

	records, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, records, 2)

	byResource := make(map[string]subscriptionRecord, len(records))
	for _, r := range records {
		byResource[r.Resource] = r
	}

	lock := byResource["/cluster/node/worker-0/sync/ptp-status/lock-state"]
	assert.Equal(t, "id-lock", lock.ID)
	assert.Equal(t, "http://consumer.example.com:9043/event", lock.Endpoint)

	// The clock-class entry has no per-resource endpoint or ID: endpoint falls back to the
	// subscriber-level EndpointUri and a fresh UUID is generated for the ID.
	clockClass := byResource["/cluster/node/worker-0/sync/sync-status/clock-class"]
	assert.Equal(t, "http://fallback.example.com:9043/event", clockClass.Endpoint)
	assert.NotEmpty(t, clockClass.ID)
}

func TestConfigMapStore_CurrentSchemaTakesPrecedence(t *testing.T) {
	// When both schemas are present, the current key is authoritative and legacy keys are ignored.
	cm := legacyV1ConfigMap(map[string]string{
		"8a1ccedf-1111-2222-3333-444455556666": `{"clientID":"x","subStore":{"store":{"/legacy":{"SubscriptionId":"old","ResourceAddress":"/legacy"}}}}`,
		configMapStoreKey:                      `[{"id":"new","resource":"/current","endpoint":"http://ep"}]`,
	})
	client := fake.NewClientset(cm)
	store, err := NewConfigMapStore(context.Background(), client, testCMNamespace, testCMName)
	require.NoError(t, err)

	records, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, subscriptionRecord{ID: "new", Resource: "/current", Endpoint: "http://ep"}, records[0])
}

func TestConfigMapStore_SaveReplacesAllKeys(t *testing.T) {
	// Save rewrites the ConfigMap from scratch: legacy v1 keys are erased, leaving only the v2 key.
	const clientID = "8a1ccedf-1111-2222-3333-444455556666"
	const legacy = `{"clientID":"x","subStore":{"store":{"/legacy":{"SubscriptionId":"old","ResourceAddress":"/legacy"}}}}`
	client := fake.NewClientset(legacyV1ConfigMap(map[string]string{clientID: legacy}))
	store, err := NewConfigMapStore(context.Background(), client, testCMNamespace, testCMName)
	require.NoError(t, err)

	require.NoError(t, store.Save([]subscriptionRecord{{ID: "new", Resource: "/current", Endpoint: "http://ep"}}))

	cm, err := client.CoreV1().ConfigMaps(testCMNamespace).Get(context.Background(), testCMName, metav1.GetOptions{})
	require.NoError(t, err)
	// Only the current-schema key remains; the legacy key is gone.
	assert.Len(t, cm.Data, 1)
	assert.NotContains(t, cm.Data, clientID)
	records, err := store.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []subscriptionRecord{{ID: "new", Resource: "/current", Endpoint: "http://ep"}}, records)
}

func TestConfigMapStore_LoadLegacySkipsUnparsableEntries(t *testing.T) {
	// A single corrupt legacy entry must not block loading the rest.
	const goodID = "8a1ccedf-1111-2222-3333-444455556666"
	good := `{"clientID":"` + goodID + `","subStore":{"store":{"/good":{"SubscriptionId":"ok","EndpointUri":"http://ep","ResourceAddress":"/good"}}}}`
	cm := legacyV1ConfigMap(map[string]string{
		goodID:                                  good,
		"bad-00000000-0000-0000-0000-000000000": "this is not json",
	})
	client := fake.NewClientset(cm)
	store, err := NewConfigMapStore(context.Background(), client, testCMNamespace, testCMName)
	require.NoError(t, err)

	records, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, subscriptionRecord{ID: "ok", Resource: "/good", Endpoint: "http://ep"}, records[0])
}
