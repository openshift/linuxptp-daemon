package cep

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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
const publishersPath = "publishers"

// supportedResourceSuffixes are the O-RAN resource suffixes this node can produce notifications for.
var supportedResourceSuffixes = []ptp.EventResource{
	ptp.PtpLockState,
	ptp.OsClockSyncState,
	ptp.PtpClockClass,
	ptp.GnssSyncStatus,
	ptp.SynceLockState,
	ptp.SynceClockQuality,
	ptp.SyncStatusState,
}

// publishableResources returns the full, node-scoped resource addresses this node publishes.
func (l *CloudEventProxy) publishableResources() []string {
	out := make([]string, 0, len(supportedResourceSuffixes))
	for _, suffix := range supportedResourceSuffixes {
		out = append(out, path.Join("/cluster/node", l.nodeName, string(suffix)))
	}
	return out
}

// isPublishedResource reports whether the ResourceAddress exactly matches a resource this node publishes
func (l *CloudEventProxy) isPublishedResource(resource string) bool {
	for _, r := range l.publishableResources() {
		if resource == r {
			return true
		}
	}
	return false
}

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
		return []event.DataValue{dv, metricDV(resourceAddr, val.Offset)}
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
	// baseURL is the externally reachable root of this API server (scheme://host:port, no path).
	// It is fixed at startup and used to build the mandatory O-RAN "UriLocation" field, which must
	// be a stable, server-determined address rather than one derived from a client request.
	baseURL  string
	nodeName string
}

// NewCloudEventProxy creates a CloudEventProxy with the given cache and pubsub. baseURL is the
// externally reachable root of the server (scheme://host:port) used to build subscription
// UriLocation values.
func NewCloudEventProxy(cache *EventCache, pubsub *PubSub, nodeName string, baseURL string) *CloudEventProxy {
	return &CloudEventProxy{cache: cache, pubSub: pubsub, nodeName: nodeName, baseURL: baseURL}
}

// handleIncomingEvents reads from r until EOF or error.
func (l *CloudEventProxy) handleIncomingEvents(r io.Reader) {
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
	mux.HandleFunc(apiBase+"health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	if l.pubSub != nil {
		subPattern := apiBase + subscriptionsPath
		mux.HandleFunc(subPattern, l.subscriptionHandler)
		mux.HandleFunc(subPattern+"/", l.subscriptionHandler)
	}
	mux.HandleFunc(apiBase+publishersPath, l.publishersHandler)
	mux.HandleFunc(apiBase, l.currentStateHandler)
	return mux
}

// AcceptDaemonConnections wait for a new connection on the listener and handles new events
func (l *CloudEventProxy) AcceptDaemonConnections(ctx context.Context, ln net.Listener) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		glog.Infof("waiting for daemon connection on %s", ln.Addr().String())
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			glog.Errorf("accept error: %v", acceptErr)
			continue
		}

		glog.Info("daemon connected, requesting current state")
		conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if encodeErr := ipc.Transmit(conn, ipc.Message{Type: ipc.TypeStatusRequest, Version: ipc.Version}); encodeErr != nil {
			glog.Errorf("failed to send status_request: %v", encodeErr)
			conn.Close()
			continue
		}
		connCtx, cancel := context.WithCancel(ctx)
		go func() { <-connCtx.Done(); conn.Close() }()
		l.handleIncomingEvents(conn)
		cancel()
		glog.Warning("daemon disconnected")
	}
}

// ListenAndServe starts the HTTP API server on the given port and blocks until
// ctx is cancelled, at which point the server is gracefully shut down.
// If certFile and keyFile are both non-empty, the server uses TLS.
func (l *CloudEventProxy) ListenAndServe(ctx context.Context, port int, certFile, keyFile string) error {
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		ReadHeaderTimeout: 5 * time.Second,
		Handler:           l.Handler(),
	}
	go func() {
		<-ctx.Done()
		if err := server.Shutdown(context.Background()); err != nil {
			glog.Errorf("API server shutdown error: %v", err)
		}
	}()
	if certFile != "" && keyFile != "" {
		glog.Infof("API server listening on :%d (TLS)", port)
		if err := server.ListenAndServeTLS(certFile, keyFile); err != nil && err != http.ErrServerClosed {
			return err
		}
	} else {
		glog.Infof("API server listening on :%d", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return err
		}
	}
	return nil
}

// problemDetails is an RFC 7807 problem object. The O-RAN O-Cloud Notification API spec (§3.1.2)
// requires error responses to be signalled with the "application/problem+json" content type.
type problemDetails struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// writeProblem emits an RFC 7807 error response. Title is the HTTP status phrase (per RFC 7807 §4.2,
// appropriate when type is "about:blank") and detail carries the human-readable explanation.
func writeProblem(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemDetails{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
}

func (l *CloudEventProxy) currentStateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	p := strings.TrimPrefix(r.URL.Path, apiBase)
	if !strings.HasSuffix(p, "/CurrentState") {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	resource := "/" + strings.TrimSuffix(p, "/CurrentState")

	e, ok := l.cache.GetBySource(resource)
	if !ok {
		writeProblem(w, http.StatusNotFound, "event notification resource is not available on this node")
		return
	}

	ce, err := e.NewCloudEventV2()
	if err != nil {
		glog.Errorf("failed to convert event to CloudEvent: %v", err)
		writeProblem(w, http.StatusInternalServerError, "internal error")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ce)
}

// publishersHandler serves GET /publishers, the CEPv1 "(Extensions to O-RAN API)" endpoint. It
// advertises the resources this node publishes (i.e. that can be subscribed to), one entry per
// supported resource, with only ResourceAddress populated.
func (l *CloudEventProxy) publishersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeProblem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	resources := l.publishableResources()
	resp := make([]publisherResponse, 0, len(resources))
	for _, r := range resources {
		resp = append(resp, publisherResponse{Resource: r})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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
	URILocation string `json:"UriLocation"`
}

// publisherResponse keeps the CEPv1 publisher schema (which reuses the subscription object shape) so
// existing clients of the GET /publishers extension still parse the response. CEPv2 publishers are
// intrinsic to the node rather than created resources, so only ResourceAddress carries a value; the
// id/endpoint/location fields exist only to preserve the schema and are returned empty.
type publisherResponse struct {
	ID          string `json:"SubscriptionId"`
	EndpointURI string `json:"EndpointUri"`
	Resource    string `json:"ResourceAddress"`
	URILocation string `json:"UriLocation"`
}

// subscriptionURILocation builds the O-RAN "UriLocation" value for a subscription: the absolute
// URL of the individual subscription resource (.../subscriptions/{id}). Per the O-Cloud
// Notification API spec (Table 30) this field is mandatory in every SubscriptionInfo response.
// It is built from the server's configured baseURL so the value is stable and independent of the
// client's request host.
func (l *CloudEventProxy) subscriptionURILocation(id string) (string, error) {
	return url.JoinPath(l.baseURL, apiBase, subscriptionsPath, id)
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
		writeProblem(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (l *CloudEventProxy) handleSubscriptionByID(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		l.handleGetSubscriptionByID(w, id)
	case http.MethodDelete:
		l.handleDeleteSubscriptionByID(w, id)
	default:
		writeProblem(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (l *CloudEventProxy) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req subscriptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.EndpointURI == "" || req.Resource == "" {
		writeProblem(w, http.StatusBadRequest, "EndpointUri and ResourceAddress are required")
		return
	}
	if _, err := url.ParseRequestURI(req.EndpointURI); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid EndpointUri")
		return
	}
	if !l.isPublishedResource(req.Resource) {
		writeProblem(w, http.StatusNotFound, "ResourceAddress is not available on this node")
		return
	}
	id, existed := l.pubSub.Subscribe(req.Resource, req.EndpointURI)
	if existed {
		writeProblem(w, http.StatusConflict, "subscription resource already exists")
		return
	}

	// Trigger the initial status notification (spec: "shall be triggered" on create). Best-effort:
	// delivery failures are logged but do not fail creation. Nothing is sent if no state is cached yet.
	if e, ok := l.cache.GetBySource(req.Resource); ok {
		l.pubSub.PublishToID(id, &e)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	uri, err := l.subscriptionURILocation(id)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "error constructing subscription URI")
		return
	}
	json.NewEncoder(w).Encode(subscriptionResponse{
		ID:          id,
		EndpointURI: req.EndpointURI,
		Resource:    req.Resource,
		URILocation: uri,
	})
}

func (l *CloudEventProxy) handleGetSubscriptions(w http.ResponseWriter, _ *http.Request) {
	subs := l.pubSub.List()
	resp := make([]subscriptionResponse, 0, len(subs))
	for _, sub := range subs {
		uri, err := l.subscriptionURILocation(sub.ID)
		if err != nil {
			writeProblem(w, http.StatusInternalServerError, "error constructing subscription URI")
			return
		}
		resp = append(resp, subscriptionResponse{
			ID:          sub.ID,
			EndpointURI: sub.Endpoint,
			Resource:    sub.Resource,
			URILocation: uri,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (l *CloudEventProxy) handleGetSubscriptionByID(w http.ResponseWriter, id string) {
	sub, ok := l.pubSub.Get(id)
	if !ok {
		writeProblem(w, http.StatusNotFound, "subscription not found")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	uri, err := l.subscriptionURILocation(sub.ID)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, "error constructing subscription URI")
		return
	}
	json.NewEncoder(w).Encode(subscriptionResponse{
		ID:          sub.ID,
		EndpointURI: sub.Endpoint,
		Resource:    sub.Resource,
		URILocation: uri,
	})
}

func (l *CloudEventProxy) handleDeleteSubscriptionByID(w http.ResponseWriter, id string) {
	if !l.pubSub.Unsubscribe(id) {
		writeProblem(w, http.StatusNotFound, "subscription not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (l *CloudEventProxy) handleDeleteSubscriptions(w http.ResponseWriter, _ *http.Request) {
	l.pubSub.UnsubscribeAll()
	w.WriteHeader(http.StatusNoContent)
}
