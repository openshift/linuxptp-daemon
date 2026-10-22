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

// EventStats is a detached, read-only snapshot for condition evaluation.
// States are evaluated source states, not the raw states on incoming events.
// ClockStates and the OS states are kept separate: a source's readiness must not
// depend on a composite state that includes a process it is about to start.
type EventStats struct {
	Windows        map[string]map[event.EventSource]utils.ROWindow
	States         map[string]map[event.EventSource]event.PTPState
	ClockStates    map[string]event.PTPState
	OSClockState   event.PTPState
	OSClockE3State event.PTPState
}

// StatsRequest identifies a clock source whose evaluated state and optional
// offset history are needed. An empty Source selects all sources on that clock.
type StatsRequest struct {
	ClockID string
	Source  event.EventSource
}

// SourceStateQuery reports whether the process identified by name and config
// is currently up according to the process manager's view of process state.
type SourceStateQuery func(name string, cfg string) bool

// StateAwareCondition can be evaluated against current process/clock state
// without replaying an event. Clock readiness is checked after data updates.
type StateAwareCondition interface {
	Condition
	// MetByCurrentState returns true when the condition is already satisfied
	// by the current state of the processes it depends on.
	MetByCurrentState(p Process, sourceUp SourceStateQuery, stats EventStats) bool
}

// Condition is a predicate that determines whether an action should be taken on a process.
type Condition interface {
	// Met returns true when this condition is satisfied by the event and the
	// clock manager's evaluated source states and offset windows.
	// p is the process that owns this condition.
	Met(p Process, ev event.Event, stats EventStats) bool
	// String returns a human-readable description for logging.
	String() string
	// GetStatsRequests identifies the clock sources whose states/windows are
	// required. An empty source requests every source on that clock.
	GetStatsRequests() []StatsRequest
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

// MetByCurrentState keeps a Never condition false during reconciliation.
func (c Never) MetByCurrentState(_ Process, _ SourceStateQuery, _ EventStats) bool {
	return false
}

// GetStatsRequests ...
func (c Never) GetStatsRequests() []StatsRequest { return []StatsRequest{} }

func eventEmpty(ev event.Event) bool {
	return ev.Source == "" && ev.CfgName == "" && ev.Data == nil && !ev.Reset
}

func condLog(p Process, c Condition, met bool, detail string) {
	name := ""
	if p != nil {
		name = p.Name()
	}
	glog.Infof("condition %s process=%q met=%v %s", c, name, met, detail)
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

// MetByCurrentState keeps an Immediate condition true during reconciliation.
func (c Immediate) MetByCurrentState(_ Process, _ SourceStateQuery, _ EventStats) bool {
	return true
}

func (c Immediate) String() string {
	return "Immediate"
}

// GetStatsRequests ...
func (c Immediate) GetStatsRequests() []StatsRequest {
	return []StatsRequest{}
}

// OnStateAndOffsetForCount is a condition that checks PTP state and offset magnitude within a rolling window.
// TODO: Move the remaining ptp4l/ts2phc source qualification into clock-owned
// filters and let conditions inspect readiness, as phc2sys does. Keep source
// readiness separate from overall clock state to avoid circular startup dependencies.
type OnStateAndOffsetForCount struct {
	ClockID    string
	ConfigName string
	Source     event.EventSource
	State      event.PTPState
	MaxOffset  float64
	Count      int
}

// Met implements the Condition interface for OnStateAndOffsetForCount.
// It returns true when the event's config/source and the evaluated source state
// match, and enough recent offsets have magnitude below MaxOffset.
func (c OnStateAndOffsetForCount) Met(p Process, ev event.Event, stats EventStats) bool {
	if !eventHasPTPState(ev.Data) || ev.Reset {
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
	return c.MetByCurrentState(p, nil, stats)
}

// MetByCurrentState evaluates source readiness from a processed-data snapshot,
// independently of the event that woke the process manager.
func (c OnStateAndOffsetForCount) MetByCurrentState(p Process, _ SourceStateQuery, stats EventStats) bool {
	if c.ConfigName == "" || c.ClockID == "" {
		return false
	}
	data, ok := stats.States[c.ClockID][c.Source]
	if !ok {
		condLog(p, c, false, "no evaluated source state")
		return false
	}
	if c.State != data {
		condLog(p, c, false, fmt.Sprintf("state want=%s got=%s", c.State, data))
		return false
	}
	w := stats.Windows[c.ClockID][c.Source]
	if w == nil {
		condLog(p, c, false, "no offset window")
		return false
	}
	nSamples := w.CountSamples(func(x float64) bool {
		return math.Abs(x) < c.MaxOffset
	})
	met := nSamples > c.Count
	condLog(p, c, met, fmt.Sprintf("window_samples=%d need>%d", nSamples, c.Count))
	return met
}

func (c OnStateAndOffsetForCount) String() string {
	return fmt.Sprintf("OnStateAndOffsetForCount source=%s cfg=%s state=%v maxOffset=%v count=%d", c.Source, c.ConfigName, c.State, c.MaxOffset, c.Count)
}

// GetStatsRequests ...
func (c OnStateAndOffsetForCount) GetStatsRequests() []StatsRequest {
	return []StatsRequest{{ClockID: c.ClockID, Source: c.Source}}
}

// OnProcessState is a condition that checks for a specific PTP synchronization state.
type OnProcessState struct {
	ConfigName string
	Source     string
	State      event.PTPState
}

// Met implements the Condition interface for OnProcessState.
// It returns true when the event identifies a source whose evaluated state matches.
func (c OnProcessState) Met(p Process, ev event.Event, stats EventStats) bool {
	if !eventHasPTPState(ev.Data) || ev.Reset {
		return false
	}
	if c.ConfigName != ev.CfgName {
		if c.Source == string(ev.Source) {
			condLog(p, c, false, fmt.Sprintf("cfg want=%s got=%s", c.ConfigName, ev.CfgName))
		}
		return false
	}
	if c.Source != "" && c.Source != string(ev.Source) {
		return false
	}
	if c.Source == "" {
		c.Source = string(ev.Source)
	}
	return c.MetByCurrentState(p, nil, stats)
}

// MetByCurrentState checks the latest evaluated source state. It never derives
// readiness from an unprocessed event or from the composite node state.
func (c OnProcessState) MetByCurrentState(p Process, _ SourceStateQuery, stats EventStats) bool {
	if c.ConfigName == "" {
		return false
	}
	for source, state := range stats.States[c.ConfigName] {
		if c.Source != "" && c.Source != string(source) {
			continue
		}
		if state == c.State {
			condLog(p, c, true, fmt.Sprintf("source=%s state=%s", source, state))
			return true
		}
	}
	return false
}

func (c OnProcessState) String() string {
	return fmt.Sprintf("OnProcessState source=%s cfg=%s state=%v", c.Source, c.ConfigName, c.State)
}

// GetStatsRequests ...
func (c OnProcessState) GetStatsRequests() []StatsRequest {
	return []StatsRequest{{ClockID: c.ConfigName, Source: event.EventSource(c.Source)}}
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

// MetByCurrentState implements StateAwareCondition for OnProcessUp.
// It returns true when the source process is currently up, which recovers
// waiters that never observed the transient PROCESS_STATUS Up event.
func (c OnProcessUp) MetByCurrentState(_ Process, sourceUp SourceStateQuery, _ EventStats) bool {
	if sourceUp == nil {
		return false
	}
	return sourceUp(string(c.Source), c.ConfigName)
}

// String returns the string representation of OnProcessUp.
func (c OnProcessUp) String() string {
	return fmt.Sprintf("OnProcessUp source=%s cfg=%s", c.Source, c.ConfigName)
}

// GetStatsRequests ...
func (c OnProcessUp) GetStatsRequests() []StatsRequest {
	return []StatsRequest{}
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

// MetByCurrentState rechecks every state-aware child against the same snapshot.
// Only event-only children use latched observations; clock readiness must not
// remain true after its source degrades while another prerequisite is pending.
func (c *All) MetByCurrentState(p Process, sourceUp SourceStateQuery, stats EventStats) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, cond := range c.Conditions {
		if sc, ok := cond.(StateAwareCondition); ok {
			if !sc.MetByCurrentState(p, sourceUp, stats) {
				return false
			}
		} else if !c.met[i] {
			return false
		}
	}
	c.met = nil
	return true
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

// GetStatsRequests ...
func (c *All) GetStatsRequests() []StatsRequest {
	seen := make(map[StatsRequest]bool)
	var result []StatsRequest
	for _, cond := range c.Conditions {
		if cond == nil {
			continue
		}
		for _, cfg := range cond.GetStatsRequests() {
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

// MetByCurrentState checks persistent alternatives only. Transient plugin or
// process-down events are not synthesized during reconciliation.
func (c Any) MetByCurrentState(p Process, sourceUp SourceStateQuery, stats EventStats) bool {
	for _, cond := range c.Conditions {
		if sc, ok := cond.(StateAwareCondition); ok && sc.MetByCurrentState(p, sourceUp, stats) {
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

// GetStatsRequests ...
func (c Any) GetStatsRequests() []StatsRequest {
	seen := make(map[StatsRequest]bool)
	var result []StatsRequest
	for _, cond := range c.Conditions {
		if cond == nil {
			continue
		}
		for _, cfg := range cond.GetStatsRequests() {
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

// GetStatsRequests ...
func (c OnPluginEvent) GetStatsRequests() []StatsRequest {
	return []StatsRequest{}
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

// GetStatsRequests ...
func (c OnProcessDown) GetStatsRequests() []StatsRequest {
	return []StatsRequest{}
}

func eventHasPTPState(data event.EventData) bool {
	switch data.(type) {
	case *event.OffsetData, *event.DPLLData, *event.StateData, *event.SyncEData:
		return true
	}
	return false
}
