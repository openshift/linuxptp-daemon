package cep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang/glog"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"os"
	"path/filepath"
)

// Store persists and retrieves subscriptionRecords for a PubSub.
type Store interface {
	Load(ctx context.Context) ([]subscriptionRecord, error)
	Save(records []subscriptionRecord) error
}

type diskStore struct {
	// path is the filepath for persistent storage of subscriptionRecords
	path string
}

// NewDiskStore returns a Store that persists subscriptionRecords to a JSON file at the given path.
func NewDiskStore(path string) (Store, error) {
	d := diskStore{path: path}
	// Create the storage file, only if it does not already exist
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("error creating directory %s: %w", filepath.Dir(path), err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return &d, nil
		}
		return nil, fmt.Errorf("unable to open Store file: %w", err)
	}
	defer file.Close()
	// Seed the new file with an empty JSON array
	if _, err = file.WriteString("[]"); err != nil {
		return nil, fmt.Errorf("unable to initialize Store file: %w", err)
	}
	return &d, nil
}

// Save writes the records to disk. The context is part of the Store interface but is unused here:
// the underlying file operations are not context-aware.
func (d *diskStore) Save(records []subscriptionRecord) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file and rename to avoid corrupting subscriptions.json on a crash mid-write.
	tmpPath := d.path + ".tmp"
	if writeErr := os.WriteFile(tmpPath, data, 0600); writeErr != nil {
		return writeErr
	}
	return os.Rename(tmpPath, d.path)
}

// Load reads the records from disk. The context is part of the Store interface but is unused here:
// the underlying file operations are not context-aware.
func (d *diskStore) Load(_ context.Context) ([]subscriptionRecord, error) {
	data, err := os.ReadFile(d.path)
	if err != nil {
		return nil, fmt.Errorf("error loading records from disk: %w", err)
	}
	// A zero-byte file (freshly created, or left by an earlier version) means no records.
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	var records []subscriptionRecord
	if err = json.Unmarshal(data, &records); err != nil {
		return nil, fmt.Errorf("error unmarshalling saved records: %w", err)
	}
	return records, nil
}

// configMapStoreKey is the key within the ConfigMap's Data map under which the
// serialized subscriptionRecords are stored.
const configMapStoreKey = "subscriptions.json"

// configMapStore persists subscriptionRecords to a Kubernetes ConfigMap via the
// API server. Unlike diskStore it survives node replacement, at the cost of an
// API-server/etcd write on every Save. It is intended to be used with a
// node-scoped ConfigMap name (e.g. one ConfigMap per DaemonSet pod) so that
// pods on different nodes do not overwrite each other's subscriptions.
type configMapStore struct {
	client    kubernetes.Interface
	namespace string
	name      string
	key       string
}

// NewConfigMapStore returns a Store backed by the named ConfigMap in the given
// namespace. The ConfigMap is created (empty) if it does not already exist, so
// that a subsequent Load has something to read. The caller supplies the
// clientset, which keeps this testable with a fake clientset.
func NewConfigMapStore(ctx context.Context, client kubernetes.Interface, namespace, name string) (Store, error) {
	c := &configMapStore{
		client:    client,
		namespace: namespace,
		name:      name,
		key:       configMapStoreKey,
	}
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string]string{c.key: "[]"},
	}
	_, err := client.CoreV1().ConfigMaps(namespace).Create(ctx, cm, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("unable to create ConfigMap store %s/%s: %w", namespace, name, err)
	}
	return c, nil
}

// Save serializes the records and writes them into the ConfigMap, replacing the Data map in its
// entirety with a single current-schema key. Any pre-existing keys are erased, so once Save runs the
// ConfigMap holds only the v2 representation: legacy cloud-event-proxy v1 per-subscriber keys are
// migrated away for good the first time subscriptions are persisted.
func (c *configMapStore) Save(records []subscriptionRecord) error {
	// Never cancel the saving, we always want this to happen, even when a shutdown signal is received
	ctx := context.TODO()
	// TODO: Figure out if we want to add a timeout to this or potentially move ConfigMap writing to a separate
	//       unblocking thread
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, getErr := c.client.CoreV1().ConfigMaps(c.namespace).Get(ctx, c.name, metav1.GetOptions{})
		if getErr != nil {
			return getErr
		}
		cm.Data = map[string]string{c.key: string(data)}
		_, updateErr := c.client.CoreV1().ConfigMaps(c.namespace).Update(ctx, cm, metav1.UpdateOptions{})
		return updateErr
	})
}

// Load reads and deserializes the records from the ConfigMap. It understands two on-disk schemas:
// the current one (a single key holding a JSON array of subscriptionRecords) and the legacy
// cloud-event-proxy v1 one (one key per subscriber, each a serialized subscriber object). The
// current schema is authoritative when present; otherwise the legacy entries are migrated.
func (c *configMapStore) Load(ctx context.Context) ([]subscriptionRecord, error) {
	cm, err := c.client.CoreV1().ConfigMaps(c.namespace).Get(ctx, c.name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error loading records from ConfigMap %s/%s: %w", c.namespace, c.name, err)
	}
	if data, ok := cm.Data[c.key]; ok && data != "" {
		var records []subscriptionRecord
		if err = json.Unmarshal([]byte(data), &records); err != nil {
			return nil, fmt.Errorf("error unmarshalling saved records: %w", err)
		}
		return records, nil
	}
	return c.loadLegacyRecords(cm.Data)
}

// TODO: Remove this in OCP 5.3 as it will no longer be possible to upgrade from a operator using the CEPv1 subscription schema
type cepV1Subscriber struct {
	ClientID    string            `json:"clientID"`
	EndPointURI string            `json:"EndpointUri"`
	SubStore    *cepV1PubSubStore `json:"subStore"`
}

type cepV1PubSubStore struct {
	Store map[string]cepV1PubSub `json:"store"`
}

type cepV1PubSub struct {
	ID          string `json:"SubscriptionId"`
	EndPointURI string `json:"EndpointUri"`
	Resource    string `json:"ResourceAddress"`
}

// loadLegacyRecords converts a cloud-event-proxy v1 ConfigMap into subscriptionRecords, allowing for an upgrade path
// from CEPv1 to CEPv2
func (c *configMapStore) loadLegacyRecords(data map[string]string) ([]subscriptionRecord, error) {
	var records []subscriptionRecord
	for key, value := range data {
		if key == c.key || value == "" {
			continue
		}
		var sub cepV1Subscriber
		if err := json.Unmarshal([]byte(value), &sub); err != nil {
			glog.Warningf("skipping unparsable legacy subscription entry %q in ConfigMap %s/%s: %v",
				key, c.namespace, c.name, err)
			continue
		}
		if sub.SubStore == nil {
			continue
		}
		for _, ps := range sub.SubStore.Store {
			if ps.Resource == "" {
				continue
			}
			endpoint := ps.EndPointURI
			if endpoint == "" {
				endpoint = sub.EndPointURI
			}
			id := ps.ID
			if id == "" {
				id = uuid.New().String()
			}
			records = append(records, subscriptionRecord{ID: id, Resource: ps.Resource, Endpoint: endpoint})
		}
	}
	return records, nil
}
