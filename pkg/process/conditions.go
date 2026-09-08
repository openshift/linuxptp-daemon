package process

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

// GnssRecoveredEventName and GnssFailoverEventName are GNSS event names for monitoring state changes.
const (
	GnssRecoveredEventName = "gnss_recovered"
	GnssFailoverEventName  = "gnss_failover"
)

// EventStats ...
type EventStats map[string]map[event.EventSource]*utils.Window

// WindowRequest ...
type WindowRequest struct {
	ClockID string
	Source  event.EventSource
}

// Condition is a predicate that determines whether an action should be taken on a process.
type Condition interface {
	// Met returns true when this condition is satisfied by the event.
	// p is the process that owns this condition.
	Met(p Process, ev event.Event, stats EventStats) bool
	// String returns a human-readable description for logging.
	String() string
	// GetWindowRequests returns the WindowRequest so that we can request the offset window stats for.
	// Empty slice means no stats are required for evaluation.
	GetWindowRequests() []WindowRequest
}

// GetCondition returns the condition for action. If no condition is
// configured, fallback is returned. Missing Restart reuses Start's condition.
func GetCondition(p Process, a Action, fallback Condition) Condition {
	if p == nil {
		return fallback
	}
	conds := p.Conditions()

	if conds == nil {
		return fallback
	}

	if c, ok := conds[a]; ok && c != nil {
		return c
	}

	if a == ActionRestart {
		if c, ok := conds[ActionStart]; ok && c != nil {
			return c
		}
	}
	return fallback
}

// Never is a condition that is never met.
type Never struct{}

// Met implements the Condition interface for Never.
// It always returns false since Never conditions are never satisfied.
func (c Never) Met(_ Process, _ event.Event, _ EventStats) bool { return false }
func (c Never) String() string                                  { return "Never" }

// GetWindowRequests ...
func (c Never) GetWindowRequests() []WindowRequest { return []WindowRequest{} }

func eventEmpty(ev event.Event) bool {
	return ev.Source == "" && ev.CfgName == "" && ev.Data == nil && !ev.Reset
}

func condLog(p Process, c Condition, met bool, detail string) {
	name := ""
	if p != nil {
		name = p.Name()
	}
	glog.Infof("condition %s process=%s met=%v %s", c, name, met, detail)
}

// Immediate is a condition that is always met immediately.
type Immediate struct {
	ConfigName string
}

// Met implements the Condition interface for Immediate.
// It always returns true since Immediate conditions are always satisfied.
func (c Immediate) Met(_ Process, _ event.Event, _ EventStats) bool {
	return true
}

func (c Immediate) String() string {
	return "Immediate"
}

// GetWindowRequests ...
func (c Immediate) GetWindowRequests() []WindowRequest {
	return []WindowRequest{}
}

// OnStateAndOffsetForCount is a condition that checks PTP state and offset magnitude within a rolling window.
type OnStateAndOffsetForCount struct {
	ClockID    string
	ConfigName string
	Source     event.EventSource
	State      event.PTPState
	MaxOffset  float64
	Count      int
}

// Met implements the Condition interface for OnStateAndOffsetForCount.
// It returns true when the event's config and state match, and enough recent samples
// in the offset window show magnitude below MaxOffset.
func (c OnStateAndOffsetForCount) Met(p Process, ev event.Event, stats EventStats) bool {
	if eventEmpty(ev) {
		return false
	}
	related := ev.CfgName == c.ConfigName || (c.Source != "" && c.Source == ev.Source)
	if c.ConfigName == "" || c.ConfigName != ev.CfgName {
		if related {
			condLog(p, c, false, fmt.Sprintf("cfg want=%s got=%s", c.ConfigName, ev.CfgName))
		}
		return false
	}
	if c.Source != "" && c.Source != ev.Source {
		condLog(p, c, false, fmt.Sprintf("source want=%s got=%s", c.Source, ev.Source))
		return false
	}
	data, ok := eventPTPState(ev.Data)
	if !ok {
		condLog(p, c, false, "event has no PTP state")
		return false
	}
	if c.State != data {
		condLog(p, c, false, fmt.Sprintf("state want=%s got=%s", c.State, data))
		return false
	}
	w := stats[c.ClockID][c.Source]
	if w == nil {
		condLog(p, c, false, "no offset window")
		return false
	}
	nSamples := w.CountSamples(func(x float64) bool {
		return math.Abs(x) < c.MaxOffset
	})
<<<<<<< HEAD
=======

>>>>>>> 32c9af18 (remove logs from pm and conds)
	met := nSamples > c.Count
	condLog(p, c, met, fmt.Sprintf("window_samples=%d need>%d", nSamples, c.Count))
	return met
}

func (c OnStateAndOffsetForCount) String() string {
	return fmt.Sprintf("OnStateAndOffsetForCount source=%s cfg=%s state=%v maxOffset=%v count=%d", c.Source, c.ConfigName, c.State, c.MaxOffset, c.Count)
}

// GetWindowRequests ...
func (c OnStateAndOffsetForCount) GetWindowRequests() []WindowRequest {
	return []WindowRequest{{ClockID: c.ClockID, Source: c.Source}}
}

// OnProcessState is a condition that checks for a specific PTP synchronization state.
type OnProcessState struct {
	ConfigName string
	Source     string
	State      event.PTPState
}

// Met implements the Condition interface for OnProcessState.
// It returns true when the event carries a PTP state payload with the expected state.
func (c OnProcessState) Met(p Process, ev event.Event, _ EventStats) bool {
	if eventEmpty(ev) {
		return false
	}
	if c.ConfigName != ev.CfgName {
		if c.Source == string(ev.Source) {
			condLog(p, c, false, fmt.Sprintf("cfg want=%s got=%s", c.ConfigName, ev.CfgName))
		}
		return false
	}
	data, ok := eventPTPState(ev.Data)
	if !ok {
		condLog(p, c, false, "event has no PTP state")
		return false
	}
	met := c.State == data
	condLog(p, c, met, fmt.Sprintf("state want=%s got=%s", c.State, data))
	return met
}

func (c OnProcessState) String() string {
	return fmt.Sprintf("OnProcessState source=%s cfg=%s state=%v", c.Source, c.ConfigName, c.State)
}

// GetWindowRequests ...
func (c OnProcessState) GetWindowRequests() []WindowRequest {
	return []WindowRequest{}
}

// OnProcessUp is true when the event is PROCESS_STATUS Up for Source / CfgName.
type OnProcessUp struct {
	ConfigName string
	Source     event.EventSource
}

// Met implements the Condition interface for OnProcessUp.
// It returns true when the event indicates the specified process is up (status = 1).
func (c OnProcessUp) Met(p Process, ev event.Event, _ EventStats) bool {
	if eventEmpty(ev) {
		return false
	}
	if c.Source != "" && ev.Source != c.Source {
		return false
	}
	if c.ConfigName != "" && ev.CfgName != c.ConfigName {
		condLog(p, c, false, fmt.Sprintf("cfg want=%s got=%s", c.ConfigName, ev.CfgName))
		return false
	}
	ps, ok := ev.Data.(*event.ProcessStatusData)
	var status int64
	if ok {
		status = ps.Status
	}
	met := ok && status == 1
	condLog(p, c, met, fmt.Sprintf("process_status=%v ok=%v", status, ok))
	return met
}

// String returns the string representation of OnProcessUp.
func (c OnProcessUp) String() string {
	return fmt.Sprintf("OnProcessUp source=%s cfg=%s", c.Source, c.ConfigName)
}

// GetWindowRequests ...
func (c OnProcessUp) GetWindowRequests() []WindowRequest {
	return []WindowRequest{}
}

// All is true when every nested condition is true for the same event.
type All struct {
	Conditions []Condition
	met        map[int]bool // Track which child conditions have been true (private field)
	mu         sync.RWMutex
}

// Met implements the Condition interface for All.
// It returns true only when all nested conditions have been true at some point (stateful).
// Resets tracking on reset events or after returning true.
func (c *All) Met(p Process, ev event.Event, stats EventStats) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Reset tracking if this is a reset event
	if ev.Reset {
		c.met = make(map[int]bool, len(c.Conditions))
		return false
	}

	// Initialize tracking on first call
	if c.met == nil {
		c.met = make(map[int]bool, len(c.Conditions))
	}

	// Check and remember which conditions are true
	for i, cond := range c.Conditions {
		if cond != nil && cond.Met(p, ev, stats) {
			c.met[i] = true
		}
	}

	// Check if ALL conditions have been true at some point
	if len(c.Conditions) == 0 {
		return true
	}
	allMet := true
	for i := range c.Conditions {
		if !c.met[i] {
			allMet = false
			break
		}
	}

	// Reset tracking after returning true (so next cycle starts fresh)
	if allMet {
		c.met = make(map[int]bool, len(c.Conditions))
		return true
	}
	return false
}

// String returns the string representation of All.
func (c *All) String() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	parts := make([]string, 0, len(c.Conditions))
	for i, cond := range c.Conditions {
		if cond == nil {
			parts = append(parts, "<nil>")
			continue
		}
		if c.met != nil {
			parts = append(parts, fmt.Sprintf("%s[%v]", cond.String(), c.met[i]))
		} else {
			parts = append(parts, cond.String())
		}
	}
	return "All(" + strings.Join(parts, ", ") + ")"
}

// GetWindowRequests ...
func (c *All) GetWindowRequests() []WindowRequest {
	seen := make(map[WindowRequest]bool)
	var result []WindowRequest
	for _, cond := range c.Conditions {
		if cond == nil {
			continue
		}
		for _, cfg := range cond.GetWindowRequests() {
			if !seen[cfg] {
				seen[cfg] = true
				result = append(result, cfg)
			}
		}
	}
	return result
}

// Any is true when at least one nested condition is true for the event.
type Any struct {
	Conditions []Condition
}

// Met implements the Condition interface for Any.
// It returns true when at least one nested condition is met by the event.
func (c Any) Met(p Process, ev event.Event, stats EventStats) bool {
	for _, cond := range c.Conditions {
		if cond != nil && cond.Met(p, ev, stats) {
			return true
		}
	}
	return false
}

// String returns the string representation of Any.
func (c Any) String() string {
	parts := make([]string, 0, len(c.Conditions))
	for _, cond := range c.Conditions {
		if cond == nil {
			parts = append(parts, "<nil>")
			continue
		}
		parts = append(parts, cond.String())
	}
	return "Any(" + strings.Join(parts, ", ") + ")"
}

// GetWindowRequests ...
func (c Any) GetWindowRequests() []WindowRequest {
	seen := make(map[WindowRequest]bool)
	var result []WindowRequest
	for _, cond := range c.Conditions {
		if cond == nil {
			continue
		}
		for _, cfg := range cond.GetWindowRequests() {
			if !seen[cfg] {
				seen[cfg] = true
				result = append(result, cfg)
			}
		}
	}
	return result
}

// OnPluginEvent is true when the event carries PluginData with a matching EventName.
type OnPluginEvent struct {
	EventName string
}

// Met implements the Condition interface for OnPluginEvent.
// It returns true when the event carries PluginData with the matching EventName.
func (c OnPluginEvent) Met(p Process, ev event.Event, _ EventStats) bool {
	if eventEmpty(ev) {
		return false
	}
	pd, ok := ev.Data.(*event.PluginData)
	if !ok {
		return false
	}
	met := pd.EventName == c.EventName
	condLog(p, c, met, fmt.Sprintf("event=%s", pd.EventName))
	return met
}

// String returns the string representation of OnPluginEvent.
func (c OnPluginEvent) String() string {
	return fmt.Sprintf("OnPluginEvent event=%s", c.EventName)
}

// GetWindowRequests ...
func (c OnPluginEvent) GetWindowRequests() []WindowRequest {
	return []WindowRequest{}
}

// OnProcessDown is true when the event is PROCESS_STATUS Down for Source / CfgName.
type OnProcessDown struct {
	ConfigName string
	Source     event.EventSource
}

// Met implements the Condition interface for OnProcessDown.
// It returns true when the event indicates the specified process is down (status = 0).
func (c OnProcessDown) Met(p Process, ev event.Event, _ EventStats) bool {
	if eventEmpty(ev) {
		return false
	}
	if c.Source != "" && ev.Source != c.Source {
		return false
	}
	if c.ConfigName != "" && ev.CfgName != c.ConfigName {
		condLog(p, c, false, fmt.Sprintf("cfg want=%s got=%s", c.ConfigName, ev.CfgName))
		return false
	}
	ps, ok := ev.Data.(*event.ProcessStatusData)
	var status int64
	if ok {
		status = ps.Status
	}
	met := ok && status == 0
	condLog(p, c, met, fmt.Sprintf("process_status=%v ok=%v", status, ok))
	return met
}

// String returns the string representation of OnProcessDown.
func (c OnProcessDown) String() string {
	return fmt.Sprintf("OnProcessDown source=%s cfg=%s", c.Source, c.ConfigName)
}

// GetWindowRequests ...
func (c OnProcessDown) GetWindowRequests() []WindowRequest {
	return []WindowRequest{}
}

func eventPTPState(data event.EventData) (event.PTPState, bool) {
	if d, ok := data.(*event.PTPData); ok {
		return d.State, true
	}
	return "", false
}
