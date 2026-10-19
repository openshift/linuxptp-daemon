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
	// testBaseURL is the server-configured root used to build subscription UriLocation values.
	testBaseURL = "http://worker-0:9043"
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
				ipc.Transmit(&buf, ipc.Message{
					Version: 99, Type: ipc.TypePTPState, Profile: testProfile,
					IFace: testIFace, Values: ipc.StateValue{State: ipc.StateLocked},
				})
				return &buf
			},
			wantCached: false,
		},
		{
			name: "invalid JSON skipped",
			input: func() io.Reader {
				var buf bytes.Buffer
				buf.WriteString("not json\n")
				ipc.Transmit(&buf, validMsg)
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
			name: "cache_clear wipes prior entries",
			input: func() io.Reader {
				var buf bytes.Buffer
				ipc.Transmit(&buf, ipc.Message{Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile, IFace: testIFace, Values: ipc.StateValue{State: ipc.StateLocked}})
				ipc.Transmit(&buf, ipc.Message{Version: ipc.Version, Type: ipc.TypeCacheClear})
				return &buf
			},
			wantCached: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := NewEventCache("worker-0")
			listener := NewCloudEventProxy(cache, nil, testBaseURL)

			listener.handleIncomingEvents(tt.input())

			_, ok := cache.Get(ipc.TypePTPState)
			assert.Equal(t, tt.wantCached, ok)
		})
	}
}

func TestIntegrationHealthCheck(t *testing.T) {
	cache := NewEventCache("worker-0")
	d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	require.NoError(t, err)
	ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
	proxy := NewCloudEventProxy(cache, ps, testBaseURL)
	apiServer := httptest.NewServer(proxy.Handler())
	defer apiServer.Close()
	resp, err := http.Get(apiServer.URL + apiBase + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "OK", string(body))
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
		d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
		require.NoError(t, err)
		ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
		proxy := NewCloudEventProxy(cache, ps, testBaseURL)
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
			proxy.handleIncomingEvents(pr)
			close(done)
		}()

		ipc.Transmit(pw, ipc.Message{
			Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
			IFace: testIFace, Values: ipc.StateValue{State: ipc.StateLocked}})
		ipc.Transmit(pw, ipc.Message{
			Version: ipc.Version, Type: ipc.TypeGNSSState, Profile: "ts2phc.0.config",
			IFace: testIFace, Values: ipc.GNSSStateValue{State: ipc.GNSSSynchronized}})
		ipc.Transmit(pw, ipc.Message{
			Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
			IFace: testIFace, Values: ipc.StateValue{State: ipc.StateFreerun}})

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
		d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
		require.NoError(t, err)
		ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
		proxy := NewCloudEventProxy(cache, ps, testBaseURL)
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
		// UriLocation is mandatory per the O-RAN O-Cloud Notification API spec and must point at
		// the individual subscription resource.
		assert.Equal(t, testBaseURL+"/api/ocloudNotifications/v2/subscriptions/"+sub.ID, sub.URILocation)

		resp, err = http.Get(apiServer.URL + "/api/ocloudNotifications/v2/subscriptions")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var subs []subscriptionResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&subs))
		resp.Body.Close()
		assert.Len(t, subs, 1)
		assert.Equal(t, testBaseURL+"/api/ocloudNotifications/v2/subscriptions/"+subs[0].ID, subs[0].URILocation)

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
		d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
		require.NoError(t, err)
		ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
		proxy := NewCloudEventProxy(cache, ps, testBaseURL)
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
		assert.Equal(t, testBaseURL+"/api/ocloudNotifications/v2/subscriptions/"+got.ID, got.URILocation)

		resp, err = http.Get(apiServer.URL + "/api/ocloudNotifications/v2/subscriptions/nonexistent-id")
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
		resp.Body.Close()
	})

	t.Run("delete by ID", func(t *testing.T) {
		cache := NewEventCache("worker-0")
		d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
		require.NoError(t, err)
		ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
		proxy := NewCloudEventProxy(cache, ps, testBaseURL)
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
	d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	require.NoError(t, err)
	ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
	proxy := NewCloudEventProxy(cache, ps, testBaseURL)
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
			name:       "unsupported resource",
			body:       `{"EndpointUri":"http://localhost:9999/event","ResourceAddress":"/resource/a"}`,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "valid subscription",
			body:       fmt.Sprintf(`{"EndpointUri":"http://localhost:9999/event","ResourceAddress":"%s"}`, testPTPLockState),
			wantStatus: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, postErr := http.Post(subURL, "application/json", strings.NewReader(tt.body))
			require.NoError(t, postErr)
			resp.Body.Close()
			assert.Equal(t, tt.wantStatus, resp.StatusCode)
		})
	}
}

func TestIntegrationCurrentState(t *testing.T) {
	cache := NewEventCache("worker-0")
	d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	require.NoError(t, err)
	ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
	proxy := NewCloudEventProxy(cache, ps, testBaseURL)

	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		proxy.handleIncomingEvents(pr)
		close(done)
	}()

	ipc.Transmit(pw, ipc.Message{
		Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
		IFace: testIFace, Values: ipc.StateValue{State: ipc.StateLocked}})
	ipc.Transmit(pw, ipc.Message{
		Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
		IFace: testIFace, Values: ipc.StateValue{State: ipc.StateFreerun}})

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

func TestErrorResponsesUseProblemJSON(t *testing.T) {
	// The O-RAN spec (§3.1.2) requires errors to be RFC 7807 "application/problem+json".
	cache := NewEventCache("worker-0")
	d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	require.NoError(t, err)
	ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
	proxy := NewCloudEventProxy(cache, ps, testBaseURL)
	apiServer := httptest.NewServer(proxy.Handler())
	defer apiServer.Close()

	cases := []struct {
		name       string
		do         func() (*http.Response, error)
		wantStatus int
	}{
		{"bad request", func() (*http.Response, error) {
			return http.Post(apiServer.URL+"/api/ocloudNotifications/v2/subscriptions", "application/json", strings.NewReader(`{}`))
		}, http.StatusBadRequest},
		{"subscription not found", func() (*http.Response, error) {
			return http.Get(apiServer.URL + "/api/ocloudNotifications/v2/subscriptions/nope")
		}, http.StatusNotFound},
		{"current state not found", func() (*http.Response, error) {
			return http.Get(apiServer.URL + "/api/ocloudNotifications/v2/" + testPTPLockState + "/CurrentState")
		}, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, doErr := tc.do()
			require.NoError(t, doErr)
			defer resp.Body.Close()
			assert.Equal(t, tc.wantStatus, resp.StatusCode)
			assert.Equal(t, "application/problem+json", resp.Header.Get("Content-Type"))

			var pd problemDetails
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&pd))
			assert.Equal(t, tc.wantStatus, pd.Status)
			assert.NotEmpty(t, pd.Title)
			assert.NotEmpty(t, pd.Detail)
		})
	}
}

func TestSubscriptionDuplicateConflict(t *testing.T) {
	cache := NewEventCache("worker-0")
	d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	require.NoError(t, err)
	ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
	proxy := NewCloudEventProxy(cache, ps, testBaseURL)
	apiServer := httptest.NewServer(proxy.Handler())
	defer apiServer.Close()

	subURL := apiServer.URL + "/api/ocloudNotifications/v2/subscriptions"
	body := fmt.Sprintf(`{"EndpointUri":"http://localhost:9999/event","ResourceAddress":%q}`, testPTPLockState)

	resp, err := http.Post(subURL, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// A second identical subscription (same resource+endpoint) must be rejected with 409.
	resp, err = http.Post(subURL, "application/json", strings.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	resp.Body.Close()
}

func TestSubscriptionDifferentClientsSameResource(t *testing.T) {
	// Duplicate detection keys on resource AND endpoint: different clients (different endpoints)
	// subscribing to the same resource must both succeed, not get a 409.
	cache := NewEventCache("worker-0")
	d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	require.NoError(t, err)
	ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
	proxy := NewCloudEventProxy(cache, ps, testNodeName, testBaseURL)
	apiServer := httptest.NewServer(proxy.Handler())
	defer apiServer.Close()

	subURL := apiServer.URL + "/api/ocloudNotifications/v2/subscriptions"
	for _, endpoint := range []string{"http://localhost:9991/event", "http://localhost:9992/event"} {
		body := fmt.Sprintf(`{"EndpointUri":%q,"ResourceAddress":%q}`, endpoint, testPTPLockState)
		resp, postErr := http.Post(subURL, "application/json", strings.NewReader(body))
		require.NoError(t, postErr)
		assert.Equal(t, http.StatusCreated, resp.StatusCode, "endpoint %s should be allowed", endpoint)
		resp.Body.Close()
	}

	// Both subscriptions should now exist.
	resp, err := http.Get(subURL)
	require.NoError(t, err)
	var subs []subscriptionResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&subs))
	resp.Body.Close()
	assert.Len(t, subs, 2)
}

func TestSubscriptionInitialNotification(t *testing.T) {
	cache := NewEventCache("worker-0")
	d, err := NewDiskStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	require.NoError(t, err)
	ps := NewPubSub(d, NewHTTPWriterFunc(2*time.Second))
	proxy := NewCloudEventProxy(cache, ps, testBaseURL)

	// Populate the cache with a current state for the resource.
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() { proxy.handleIncomingEvents(pr); close(done) }()
	ipc.Transmit(pw, ipc.Message{
		Version: ipc.Version, Type: ipc.TypePTPState, Profile: testProfile,
		IFace: testIFace, Values: ipc.StateValue{State: ipc.StateLocked}})
	pw.Close()
	<-done

	// The consumer endpoint captures whatever is pushed to it.
	received := make(chan []byte, 1)
	consumer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- b
		w.WriteHeader(http.StatusNoContent)
	}))
	defer consumer.Close()

	apiServer := httptest.NewServer(proxy.Handler())
	defer apiServer.Close()

	subBody := fmt.Sprintf(`{"EndpointUri":%q,"ResourceAddress":%q}`, consumer.URL, testPTPLockState)
	resp, err := http.Post(apiServer.URL+"/api/ocloudNotifications/v2/subscriptions",
		"application/json", strings.NewReader(subBody))
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	resp.Body.Close()

	// Creating the subscription must have pushed the current state to the consumer endpoint.
	select {
	case b := <-received:
		var got ce.Event
		require.NoError(t, json.Unmarshal(b, &got))
		assert.Contains(t, got.Source(), "ptp-status/lock-state")
		assert.NotEmpty(t, got.Data())
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive initial notification")
	}
}
