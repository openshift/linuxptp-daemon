package cep

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/redhat-cne/sdk-go/pkg/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
)

const (
	testProfile      = "ptp4l.0.config"
	testIFace        = "ens2f0"
	testPTPLockState = "/cluster/node/worker-0/sync/ptp-status/lock-state"
)

func TestBuildDataValues(t *testing.T) {
	resource := "/cluster/node/worker-0/ens2f0/master"

	tests := []struct {
		name     string
		resource string
		value    ipc.Value
		want     []event.DataValue
	}{
		{
			name:     "ptp state: state + offset placeholder",
			resource: resource,
			value:    ipc.StateValue{State: ipc.StateLocked},
			want: []event.DataValue{
				{Resource: resource, DataType: event.NOTIFICATION, ValueType: event.ENUMERATION, Value: ipc.StateLocked},
				{Resource: resource, DataType: event.METRIC, ValueType: event.DECIMAL, Value: placeholderOffset},
			},
		},
		{
			name:     "os clock state: state + offset placeholder",
			resource: "/cluster/node/worker-0/CLOCK_REALTIME",
			value:    ipc.StateValue{State: ipc.StateFreerun},
			want: []event.DataValue{
				{Resource: "/cluster/node/worker-0/CLOCK_REALTIME", DataType: event.NOTIFICATION, ValueType: event.ENUMERATION, Value: ipc.StateFreerun},
				{Resource: "/cluster/node/worker-0/CLOCK_REALTIME", DataType: event.METRIC, ValueType: event.DECIMAL, Value: placeholderOffset},
			},
		},
		{
			name:     "gnss state: state + offset placeholder + gpsFix placeholder",
			resource: resource,
			value:    ipc.GNSSStateValue{State: ipc.GNSSSynchronized},
			want: []event.DataValue{
				{Resource: resource, DataType: event.NOTIFICATION, ValueType: event.ENUMERATION, Value: ipc.GNSSSynchronized},
				{Resource: resource, DataType: event.METRIC, ValueType: event.DECIMAL, Value: placeholderOffset},
				{Resource: resource + "/gpsFix", DataType: event.METRIC, ValueType: event.DECIMAL, Value: placeholderOffset},
			},
		},
		{
			name:     "synce state: metric/decimal type",
			resource: "/cluster/node/worker-0/ens7f0",
			value:    ipc.SyncEStateValue{State: ipc.StateLocked},
			want: []event.DataValue{
				{Resource: "/cluster/node/worker-0/ens7f0", DataType: event.METRIC, ValueType: event.DECIMAL, Value: ipc.StateLocked},
			},
		},
		{
			name:     "sync state: state only, no offset",
			resource: "/cluster/node/worker-0/sync-status/sync-state",
			value:    ipc.SyncStateValue{State: ipc.StateLocked},
			want: []event.DataValue{
				{Resource: "/cluster/node/worker-0/sync-status/sync-state", DataType: event.NOTIFICATION, ValueType: event.ENUMERATION, Value: ipc.StateLocked},
			},
		},
		{
			name:     "clock class: single metric value",
			resource: "/cluster/node/worker-0/ens2f0/master/clock-class",
			value:    ipc.ClockClassValue{ClockClass: 6},
			want: []event.DataValue{
				{Resource: "/cluster/node/worker-0/ens2f0/master/clock-class", DataType: event.METRIC, ValueType: event.DECIMAL, Value: int64(6)},
			},
		},
		{
			name:     "synce clock quality: separate Ql and extQl",
			resource: "/cluster/node/worker-0/ens7f0",
			value:    ipc.SyncEClockQualityValue{QL: 1, ExtendedQL: 0x20},
			want: []event.DataValue{
				{Resource: "/cluster/node/worker-0/ens7f0/Ql", DataType: event.METRIC, ValueType: event.DECIMAL, Value: float64(1)},
				{Resource: "/cluster/node/worker-0/ens7f0/extQl", DataType: event.METRIC, ValueType: event.DECIMAL, Value: float64(0x20)},
			},
		},
		{
			name:     "nil value returns nil",
			resource: resource,
			value:    nil,
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildDataValues(tt.resource, tt.value)
			require.Equal(t, len(tt.want), len(got))
			for i, wantDV := range tt.want {
				assert.Equal(t, wantDV.Resource, got[i].Resource, "dv[%d] resource", i)
				assert.Equal(t, wantDV.DataType, got[i].DataType, "dv[%d] dataType", i)
				assert.Equal(t, wantDV.ValueType, got[i].ValueType, "dv[%d] valueType", i)
				assert.Equal(t, wantDV.Value, got[i].Value, "dv[%d] value", i)
			}
		})
	}
}

func TestListenerResilience(t *testing.T) {
	validMsg := ipc.Message{
		Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
		IFace: testIFace, Values: ipc.StateValue{State: ipc.StateLocked},
	}

	tests := []struct {
		name       string
		input      func() io.Reader
		wantCached bool
	}{
		{
			name: "version mismatch ignored",
			input: func() io.Reader {
				var buf bytes.Buffer
				ipc.Encode(&buf, []ipc.Message{{
					Version: 99, Type: ipc.TypePTPState, Profile: testProfile,
					IFace: testIFace, Values: ipc.StateValue{State: ipc.StateLocked},
				}})
				return &buf
			},
			wantCached: false,
		},
		{
			name: "invalid JSON skipped",
			input: func() io.Reader {
				var buf bytes.Buffer
				buf.WriteString("not json\n")
				ipc.Encode(&buf, []ipc.Message{validMsg})
				return &buf
			},
			wantCached: true,
		},
		{
			name: "empty lines skipped",
			input: func() io.Reader {
				data, _ := json.Marshal(validMsg)
				return strings.NewReader("\n\n" + string(data) + "\n\n")
			},
			wantCached: true,
		},
		{
			name: "status_response not cached",
			input: func() io.Reader {
				var buf bytes.Buffer
				ipc.Encode(&buf, []ipc.Message{{
					Version: ipc.Version,
					Type:    ipc.TypeStatusResponse,
				}})
				return &buf
			},
			wantCached: false,
		},
		{
			name: "cache_clear wipes prior entries",
			input: func() io.Reader {
				var buf bytes.Buffer
				ipc.Encode(&buf, []ipc.Message{
					{Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
						IFace: testIFace, Values: ipc.StateValue{State: ipc.StateLocked}},
					{Version: ipc.Version, Type: ipc.TypeCacheClear},
				})
				return &buf
			},
			wantCached: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := NewEventCache("worker-0")
			listener := NewCloudEventProxy(cache, nil)

			listener.Listen(tt.input())

			_, ok := cache.Get(ipc.TypePTPState)
			assert.Equal(t, tt.wantCached, ok)
		})
	}
}

func TestIntegrationEventDelivery(t *testing.T) {
	t.Run("subscriber receives matching events", func(t *testing.T) {
		var mu sync.Mutex
		var received []ce.Event

		consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			var e ce.Event
			if unmarshalErr := json.Unmarshal(body, &e); unmarshalErr != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			received = append(received, e)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		}))
		defer consumer.Close()

		cache := NewEventCache("worker-0")
		ps := NewPubSub(filepath.Join(t.TempDir(), "subscriptions.json"), NewHTTPWriterFunc(2*time.Second))
		proxy := NewCloudEventProxy(cache, ps)
		apiServer := httptest.NewServer(proxy.Handler())
		defer apiServer.Close()

		subBody := fmt.Sprintf(`{"EndpointUri":"%s/event","ResourceAddress":"%s"}`, consumer.URL, testPTPLockState)
		resp, err := http.Post(
			apiServer.URL+"/api/ocloudNotifications/v2/subscriptions",
			"application/json",
			strings.NewReader(subBody),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		resp.Body.Close()

		pr, pw := io.Pipe()
		done := make(chan struct{})
		go func() {
			proxy.Listen(pr)
			close(done)
		}()

		ipc.Encode(pw, []ipc.Message{
			{Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
				IFace: testIFace, Values: ipc.StateValue{State: ipc.StateLocked}},
			{Version: ipc.Version, Type: ipc.TypeGNSSState, Profile: "ts2phc.0.config",
				IFace: testIFace, Values: ipc.GNSSStateValue{State: ipc.GNSSSynchronized}},
			{Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
				IFace: testIFace, Values: ipc.StateValue{State: ipc.StateFreerun}},
		})

		pw.Close()
		<-done

		require.Eventually(t, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(received) >= 2
		}, 2*time.Second, 50*time.Millisecond, "expected at least 2 events delivered to consumer")

		mu.Lock()
		defer mu.Unlock()

		for _, e := range received {
			assert.Equal(t, "1.0", e.SpecVersion())
			assert.Contains(t, e.Source(), "worker-0")
			assert.Contains(t, e.Source(), string("ptp-status/lock-state"))
			assert.NotEmpty(t, e.Data())
		}

		assert.Equal(t, 2, len(received), "consumer should receive exactly 2 PTP events, not the GNSS event")
	})
}

func TestIntegrationSubscriptionAPI(t *testing.T) {
	t.Run("CRUD lifecycle", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		ps := NewPubSub(filepath.Join(t.TempDir(), "subscriptions.json"), NewHTTPWriterFunc(2*time.Second))
		proxy := NewCloudEventProxy(cache, ps)
		apiServer := httptest.NewServer(proxy.Handler())
		defer apiServer.Close()

		subBody := fmt.Sprintf(`{"EndpointUri":"http://localhost:9999/event","ResourceAddress":"%s"}`, testPTPLockState)

		resp, err := http.Post(
			apiServer.URL+"/api/ocloudNotifications/v2/subscriptions",
			"application/json",
			strings.NewReader(subBody),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var sub subscriptionResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&sub))
		resp.Body.Close()
		assert.NotEmpty(t, sub.ID)
		assert.Equal(t, testPTPLockState, sub.Resource)
		assert.Equal(t, "http://localhost:9999/event", sub.EndpointURI)

		resp, err = http.Get(apiServer.URL + "/api/ocloudNotifications/v2/subscriptions")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var subs []subscriptionResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&subs))
		resp.Body.Close()
		assert.Len(t, subs, 1)

		req, _ := http.NewRequest(http.MethodDelete, apiServer.URL+"/api/ocloudNotifications/v2/subscriptions", nil)
		resp, err = http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		resp.Body.Close()

		resp, err = http.Get(apiServer.URL + "/api/ocloudNotifications/v2/subscriptions")
		require.NoError(t, err)
		var subsAfter []subscriptionResponse
		json.NewDecoder(resp.Body).Decode(&subsAfter)
		resp.Body.Close()
		assert.Empty(t, subsAfter)
	})

	t.Run("get by ID", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		ps := NewPubSub(filepath.Join(t.TempDir(), "subscriptions.json"), NewHTTPWriterFunc(2*time.Second))
		proxy := NewCloudEventProxy(cache, ps)
		apiServer := httptest.NewServer(proxy.Handler())
		defer apiServer.Close()

		subBody := fmt.Sprintf(`{"EndpointUri":"http://localhost:9999/event","ResourceAddress":"%s"}`, testPTPLockState)

		resp, err := http.Post(
			apiServer.URL+"/api/ocloudNotifications/v2/subscriptions",
			"application/json",
			strings.NewReader(subBody),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var created subscriptionResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
		resp.Body.Close()

		resp, err = http.Get(apiServer.URL + "/api/ocloudNotifications/v2/subscriptions/" + created.ID)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var got subscriptionResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
		resp.Body.Close()
		assert.Equal(t, created.ID, got.ID)
		assert.Equal(t, testPTPLockState, got.Resource)

		resp, err = http.Get(apiServer.URL + "/api/ocloudNotifications/v2/subscriptions/nonexistent-id")
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("delete by ID", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		ps := NewPubSub(filepath.Join(t.TempDir(), "subscriptions.json"), NewHTTPWriterFunc(2*time.Second))
		proxy := NewCloudEventProxy(cache, ps)
		apiServer := httptest.NewServer(proxy.Handler())
		defer apiServer.Close()

		subBody := fmt.Sprintf(`{"EndpointUri":"http://localhost:9999/event","ResourceAddress":"%s"}`, testPTPLockState)

		resp, err := http.Post(
			apiServer.URL+"/api/ocloudNotifications/v2/subscriptions",
			"application/json",
			strings.NewReader(subBody),
		)
		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, resp.StatusCode)

		var created subscriptionResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&created))
		resp.Body.Close()

		req, _ := http.NewRequest(http.MethodDelete, apiServer.URL+"/api/ocloudNotifications/v2/subscriptions/"+created.ID, nil)
		resp, err = http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNoContent, resp.StatusCode)
		resp.Body.Close()

		resp, err = http.Get(apiServer.URL + "/api/ocloudNotifications/v2/subscriptions")
		require.NoError(t, err)
		var subs []subscriptionResponse
		json.NewDecoder(resp.Body).Decode(&subs)
		resp.Body.Close()
		assert.Empty(t, subs)

		req, _ = http.NewRequest(http.MethodDelete, apiServer.URL+"/api/ocloudNotifications/v2/subscriptions/"+created.ID, nil)
		resp, err = http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	})
}

func TestSubscriptionValidation(t *testing.T) {
	cache := NewEventCache("worker-0")
	ps := NewPubSub(filepath.Join(t.TempDir(), "subscriptions.json"), NewHTTPWriterFunc(2*time.Second))
	proxy := NewCloudEventProxy(cache, ps)
	apiServer := httptest.NewServer(proxy.Handler())
	defer apiServer.Close()

	subURL := apiServer.URL + "/api/ocloudNotifications/v2/subscriptions"

	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "invalid endpoint URI",
			body:       `{"EndpointUri":"not a url","ResourceAddress":"/resource/a"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing endpoint URI",
			body:       `{"ResourceAddress":"/resource/a"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "missing resource",
			body:       `{"EndpointUri":"http://localhost:9999/event"}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "valid subscription",
			body:       `{"EndpointUri":"http://localhost:9999/event","ResourceAddress":"/resource/a"}`,
			wantStatus: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := http.Post(subURL, "application/json", strings.NewReader(tt.body))
			require.NoError(t, err)
			resp.Body.Close()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)
		})
	}
}

func TestIntegrationCurrentState(t *testing.T) {
	cache := NewEventCache("worker-0")
	ps := NewPubSub(filepath.Join(t.TempDir(), "subscriptions.json"), NewHTTPWriterFunc(2*time.Second))
	proxy := NewCloudEventProxy(cache, ps)

	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		proxy.Listen(pr)
		close(done)
	}()

	ipc.Encode(pw, []ipc.Message{
		{Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
			IFace: testIFace, Values: ipc.StateValue{State: ipc.StateLocked}},
		{Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
			IFace: testIFace, Values: ipc.StateValue{State: ipc.StateFreerun}},
	})

	pw.Close()
	<-done

	apiServer := httptest.NewServer(proxy.Handler())
	defer apiServer.Close()

	resp, err := http.Get(apiServer.URL + "/api/ocloudNotifications/v2/" + testPTPLockState + "/CurrentState")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got ce.Event
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	resp.Body.Close()

	assert.Equal(t, "1.0", got.SpecVersion())
	assert.Contains(t, got.Source(), "worker-0")
	assert.Contains(t, got.Source(), "ptp-status/lock-state")
	assert.NotEmpty(t, got.Data())

	resp, err = http.Get(apiServer.URL + "/api/ocloudNotifications/v2/nonexistent/resource/CurrentState")
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}
