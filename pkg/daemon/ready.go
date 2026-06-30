package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/alias"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilwait "k8s.io/apimachinery/pkg/util/wait"
)

type ReadyTracker struct {
	mutex          sync.Mutex
	config         bool
	processManager *ProcessManager
}

func (rt *ReadyTracker) Ready() (bool, string) {
	rt.mutex.Lock()
	defer rt.mutex.Unlock()

	if !rt.config {
		return false, "Config not applied"
	}

	if len(rt.processManager.process) == 0 {
		return false, "No processes have started"
	}

	notRunning := strings.Builder{}
	noMetrics := strings.Builder{}
	activeCount := 0
	for _, p := range rt.processManager.process {
		if p == nil || p.skipInitialStartup != "" {
			continue
		}
		activeCount++
		if p.Stopped() {
			if notRunning.Len() > 0 {
				notRunning.WriteString(", ")
			}
			notRunning.WriteString(p.name)
		} else if !p.hasCollectedMetrics {
			if noMetrics.Len() > 0 {
				noMetrics.WriteString(", ")
			}
			noMetrics.WriteString(p.name)
		}

	}
	if activeCount == 0 {
		return false, "No processes have started"
	}

	if notRunning.Len() > 0 {
		return false, "Stopped process(es): " + notRunning.String()
	}

	if noMetrics.Len() > 0 {
		return false, "Process(es) have not yet collected metrics: " + noMetrics.String()
	}

	return true, ""
}

func (rt *ReadyTracker) setConfig(v bool) {
	rt.mutex.Lock()
	rt.config = v
	rt.mutex.Unlock()
}

type readyHandler struct {
	tracker *ReadyTracker
}

func (h readyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if isReady, msg := h.tracker.Ready(); !isReady {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, "503: %s\n", msg)
	} else {
		w.WriteHeader(http.StatusOK)
	}
}

type metricHandler struct {
	tracker *ReadyTracker
}

func (h metricHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	glog.V(14).Info("/emit-logs: handler invoked")
	if isReady, _ := h.tracker.Ready(); !isReady {
		glog.V(14).Info("/emit-logs: not ready, opening gate early and returning 204")
		w.WriteHeader(http.StatusNoContent)
		// Open the gate even when not ready: processes need to start writing
		// to collect metrics and become ready. No stale state exists yet, so
		// there is nothing harmful to replay.
		if dn := h.tracker.processManager.daemon; dn != nil {
			dn.liveGate.Open()
		}
		return
	}
	w.WriteHeader(http.StatusOK)

	// Emit replay data synchronously so the live gate is only opened after
	// all replay state has been written to the socket.
	glog.V(14).Info("/emit-logs: starting synchronous replay (EmitClockSyncLogs + EmitPortRoleLogs)")
	eventHandler := h.tracker.processManager.ptpEventHandler
	eventHandler.EmitClockSyncLogs()
	eventHandler.EmitPortRoleLogs()
	glog.V(14).Info("/emit-logs: synchronous replay complete, opening liveGate")

	// Open the live gate: ptp4l processes may now write to the socket.
	if dn := h.tracker.processManager.daemon; dn != nil {
		dn.liveGate.Open()
	}

	// Non-critical emits can remain async.
	glog.V(14).Info("/emit-logs: firing async emits (ProcessStatusLogs, ClockClassLogs)")
	processManager := h.tracker.processManager
	go processManager.EmitProcessStatusLogs()
	go processManager.EmitClockClassLogs()
}

type portAliasesHandler struct{}

func (h portAliasesHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	aliases := alias.GetAllAliases()
	if err := json.NewEncoder(w).Encode(aliases); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// StartReadyServer ...
func StartReadyServer(bindAddress string, tracker *ReadyTracker, serveInitMetrics bool) {
	glog.Info("Starting Ready Server")
	mux := http.NewServeMux()
	mux.Handle("/ready", readyHandler{tracker: tracker})
	mux.Handle("/port-aliases", portAliasesHandler{})
	if serveInitMetrics {
		mux.Handle("/emit-logs", metricHandler{tracker: tracker})
	}
	go utilwait.Until(func() {
		err := http.ListenAndServe(bindAddress, mux)
		if err != nil {
			utilruntime.HandleError(fmt.Errorf("starting metrics server failed: %v", err))
		}
	}, 5*time.Second, utilwait.NeverStop)
}
