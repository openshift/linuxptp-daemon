package cep

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ipc"
	"github.com/redhat-cne/sdk-go/pkg/event"
	"github.com/redhat-cne/sdk-go/pkg/event/ptp"

	"github.com/golang/glog"
)

const apiBase = "/api/ocloudNotifications/v2/"
const subscriptionsPath = "subscriptions"

// oranMapping maps IPC events to the appropriate CloudEvent
func oranMapping(ipcType string) (eventType ptp.EventType, source ptp.EventResource, ok bool) {
	switch ipcType {
	case ipc.TypePTPState:
		return ptp.PtpStateChange, ptp.PtpLockState, true
	case ipc.TypeOSClockState:
		return ptp.OsClockSyncStateChange, ptp.OsClockSyncState, true
	case ipc.TypeClockClass:
		return ptp.PtpClockClassChange, ptp.PtpClockClass, true
	case ipc.TypeGNSSState:
		return ptp.GnssStateChange, ptp.GnssSyncStatus, true
	case ipc.TypeSyncEState:
		return ptp.SynceStateChange, ptp.SynceLockState, true
	case ipc.TypeSyncEClockQuality:
		return ptp.SynceClockQualityChange, ptp.SynceClockQuality, true
	case ipc.TypeSyncState:
		return ptp.SyncStateChange, ptp.SyncStatusState, true
	default:
		return "", "", false
	}
}

// copyEvent returns a deep copy of e
func copyEvent(e *event.Event) event.Event {
	out := event.Event{
		ID:              e.ID,
		Type:            e.Type,
		Source:          e.Source,
		DataContentType: e.DataContentType,
		DataSchema:      e.DataSchema,
	}
	if e.Time != nil {
		t := *e.Time
		out.Time = &t
	}
	if e.Data != nil {
		out.SetData(*e.Data)
	}
	return out
}

func metricDV(resource string, value interface{}) event.DataValue {
	return event.DataValue{
		Resource:  resource,
		DataType:  event.METRIC,
		ValueType: event.DECIMAL,
		Value:     value,
	}
}

// TODO: replace placeholder offsets with real values from the daemon via IPC.
// Offsets are not part of the O-RAN spec but were included in cloud-event-proxy v1.
// Including offsets in state change events either makes them noisier (firing on every
// offset change) or loses offset tracking when the state is not changing. A separate
// IPC message for periodic offset reporting would avoid both problems and should be considered.
// Otherwise unless applications are given prometheus metric access this will be invisible to
// them. If applications need offset visibility it should be given first class API support,
// periodic summaries with min/max/avg over a window.
const placeholderOffset = int64(0)

func buildDataValues(resourceAddr string, v ipc.Value) []event.DataValue {
	switch val := v.(type) {
	case ipc.StateValue:
		dv := event.DataValue{
			Resource:  resourceAddr,
			DataType:  event.NOTIFICATION,
			ValueType: event.ENUMERATION,
			Value:     val.State,
		}
		return []event.DataValue{dv, metricDV(resourceAddr, placeholderOffset)}
	case ipc.GNSSStateValue:
		dv := event.DataValue{
			Resource:  resourceAddr,
			DataType:  event.NOTIFICATION,
			ValueType: event.ENUMERATION,
			Value:     val.State,
		}
		return []event.DataValue{dv,
			metricDV(resourceAddr, placeholderOffset),
			metricDV(path.Join(resourceAddr, "gpsFix"), placeholderOffset),
		}
	case ipc.SyncStateValue:
		return []event.DataValue{{
			Resource:  resourceAddr,
			DataType:  event.NOTIFICATION,
			ValueType: event.ENUMERATION,
			Value:     val.State,
		}}
	case ipc.SyncEStateValue:
		return []event.DataValue{metricDV(resourceAddr, val.State)}
	case ipc.ClockClassValue:
		return []event.DataValue{metricDV(resourceAddr, int64(val.ClockClass))}
	case ipc.SyncEClockQualityValue:
		return []event.DataValue{
			metricDV(path.Join(resourceAddr, "Ql"), float64(val.QL)),
			metricDV(path.Join(resourceAddr, "extQl"), float64(val.ExtendedQL)),
		}
	default:
		return nil
	}
}

func setDataValue(e *event.Event, dv event.DataValue) bool {
	for i, existing := range e.Data.Values {
		if existing.Resource == dv.Resource && existing.DataType == dv.DataType {
			if existing.Value == dv.Value {
				return false
			}
			e.Data.Values[i] = dv
			return true
		}
	}
	e.Data.AppendValues(dv)
	return true
}

// CloudEventProxy connects PubSub, EventCache, and the REST API.
type CloudEventProxy struct {
	cache  *EventCache
	pubSub *PubSub
}

// NewCloudEventProxy creates a CloudEventProxy with the given cache and pubsub.
func NewCloudEventProxy(cache *EventCache, pubsub *PubSub) *CloudEventProxy {
	return &CloudEventProxy{cache: cache, pubSub: pubsub}
}

// Listen reads from r, and handles events written to the stream.
func (l *CloudEventProxy) Listen(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg ipc.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			glog.Errorf("IPC unmarshal error, skipping message: %v", err)
			continue
		}
		if msg.Version != ipc.Version {
			glog.Warningf("IPC version mismatch: got %d, want %d", msg.Version, ipc.Version)
			continue
		}

		switch msg.Type {
		case ipc.TypeCacheClear:
			glog.Info("received cache_clear from daemon, clearing event cache")
			l.cache.Clear()
			continue
		case ipc.TypeStatusResponse:
			// TODO: handle this
			glog.Info("received status_response from daemon")
			continue
		case ipc.TypeStatusRequest:
			glog.Warning("received status_request from daemon (unexpected direction)")
			continue
		}

		if newEvent := l.cache.Update(msg); newEvent != nil && l.pubSub != nil {
			l.pubSub.Publish(newEvent)
		}
	}
	if err := scanner.Err(); err != nil {
		glog.Errorf("IPC reader error: %v", err)
	}
}

// Handler returns the HTTP handler for the CloudEvent REST API.
func (l *CloudEventProxy) Handler() http.Handler {
	mux := http.NewServeMux()
	if l.pubSub != nil {
		subPattern := apiBase + subscriptionsPath
		mux.HandleFunc(subPattern, l.subscriptionHandler)
		mux.HandleFunc(subPattern+"/", l.subscriptionHandler)
	}
	mux.HandleFunc(apiBase, l.currentStateHandler)
	return mux
}

// ListenAndServe starts the HTTP API server on the given port.
func (l *CloudEventProxy) ListenAndServe(port int) {
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		ReadHeaderTimeout: 5 * time.Second,
		Handler:           l.Handler(),
	}
	glog.Infof("API server listening on :%d", port)
	if err := server.ListenAndServe(); err != nil {
		glog.Errorf("API server error: %v", err)
	}
}

func (l *CloudEventProxy) currentStateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	p := strings.TrimPrefix(r.URL.Path, apiBase)
	if !strings.HasSuffix(p, "/CurrentState") {
		http.NotFound(w, r)
		return
	}
	resource := "/" + strings.TrimSuffix(p, "/CurrentState")

	e, ok := l.cache.GetBySource(resource)
	if !ok {
		http.NotFound(w, r)
		return
	}

	ce, err := e.NewCloudEventV2()
	if err != nil {
		glog.Errorf("failed to convert event to CloudEvent: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ce)
}

// --- Subscription HTTP handlers ---

type httpWriter struct {
	endpoint string
	client   *http.Client
}

// WriterFunc creates an io.Writer for a given endpoint.
type WriterFunc func(endpoint string) io.Writer

// NewHTTPWriter returns an io.Writer that POSTs data to the given endpoint.
func NewHTTPWriter(endpoint string, client *http.Client) io.Writer {
	return &httpWriter{endpoint: endpoint, client: client}
}

// NewHTTPWriterFunc returns a WriterFunc that creates HTTP POST writers with the given timeout.
func NewHTTPWriterFunc(timeout time.Duration) WriterFunc {
	return func(endpoint string) io.Writer {
		return NewHTTPWriter(endpoint, &http.Client{Timeout: timeout})
	}
}

func (hw *httpWriter) Write(p []byte) (int, error) {
	resp, err := hw.client.Post(hw.endpoint, "application/json", bytes.NewReader(p))
	if err != nil {
		return 0, err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("subscriber %s returned status %d", hw.endpoint, resp.StatusCode)
	}
	return len(p), nil
}

type subscriptionRequest struct {
	EndpointURI string `json:"EndpointUri"`
	Resource    string `json:"ResourceAddress"`
}

type subscriptionResponse struct {
	ID          string `json:"SubscriptionId"`
	EndpointURI string `json:"EndpointUri"`
	Resource    string `json:"ResourceAddress"`
}

func (l *CloudEventProxy) subscriptionHandler(w http.ResponseWriter, r *http.Request) {
	subPath := strings.TrimPrefix(r.URL.Path, apiBase+subscriptionsPath)
	subPath = strings.TrimPrefix(subPath, "/")

	if subPath == "" {
		l.handleSubscriptions(w, r)
	} else {
		l.handleSubscriptionByID(w, r, subPath)
	}
}

func (l *CloudEventProxy) handleSubscriptions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		l.handleCreateSubscription(w, r)
	case http.MethodGet:
		l.handleGetSubscriptions(w, r)
	case http.MethodDelete:
		l.handleDeleteSubscriptions(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (l *CloudEventProxy) handleSubscriptionByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		l.handleGetSubscriptionByID(w, id)
	case http.MethodDelete:
		l.handleDeleteSubscriptionByID(w, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (l *CloudEventProxy) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req subscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.EndpointURI == "" || req.Resource == "" {
		http.Error(w, "EndpointUri and ResourceAddress are required", http.StatusBadRequest)
		return
	}
	if _, err := url.ParseRequestURI(req.EndpointURI); err != nil {
		http.Error(w, "invalid EndpointUri", http.StatusBadRequest)
		return
	}

	id := l.pubSub.Subscribe(req.Resource, req.EndpointURI)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(subscriptionResponse{
		ID:          id,
		EndpointURI: req.EndpointURI,
		Resource:    req.Resource,
	})
}

func (l *CloudEventProxy) handleGetSubscriptions(w http.ResponseWriter, _ *http.Request) {
	subs := l.pubSub.List()
	resp := make([]subscriptionResponse, 0, len(subs))
	for _, sub := range subs {
		resp = append(resp, subscriptionResponse{
			ID:          sub.ID,
			EndpointURI: sub.Endpoint,
			Resource:    sub.Resource,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (l *CloudEventProxy) handleGetSubscriptionByID(w http.ResponseWriter, id string) {
	sub, ok := l.pubSub.Get(id)
	if !ok {
		http.Error(w, "subscription not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(subscriptionResponse{
		ID:          sub.ID,
		EndpointURI: sub.Endpoint,
		Resource:    sub.Resource,
	})
}

func (l *CloudEventProxy) handleDeleteSubscriptionByID(w http.ResponseWriter, id string) {
	if !l.pubSub.Unsubscribe(id) {
		http.Error(w, "subscription not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *CloudEventProxy) handleDeleteSubscriptions(w http.ResponseWriter, _ *http.Request) {
	l.pubSub.UnsubscribeAll()
	w.WriteHeader(http.StatusNoContent)
}
