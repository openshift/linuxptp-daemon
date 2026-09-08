package process

import (
	"context"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

const (
	ttestPtp4lConditions  = "ptp4l.0.config"
	ttestTs2phcConditions = "ts2phc.0.config"
)

type stubProc struct {
	conds map[Action]Condition
}

func (s *stubProc) Name() string                     { return "stub" }
func (s *stubProc) ConfigName() string               { return ttestPtp4lConditions }
func (s *stubProc) Start(context.Context) error      { return nil }
func (s *stubProc) Stop() error                      { return nil }
func (s *stubProc) Conditions() map[Action]Condition { return s.conds }
func (s *stubProc) State() State                     { return Created }
func (s *stubProc) Profile() *ptpv1.PtpProfile       { return &ptpv1.PtpProfile{} }
func (s *stubProc) ClockType() event.ClockType       { return event.OC }
func (s *stubProc) DependentProcesses() []Process    { return nil }

func TestGetCondition_MissingStartIsImmediate(t *testing.T) {
	p := &stubProc{}
	assert.Equal(t, Immediate{}, GetCondition(p, ActionStart, Immediate{}))
	assert.True(t, GetCondition(p, ActionStart, Immediate{}).Met(p, event.Event{}, EventStats{}))
}

func TestGetCondition_MissingStopUseFallback(t *testing.T) {
	p := &stubProc{}
	assert.Equal(t, Never{}, GetCondition(p, ActionStop, Never{}))
	assert.False(t, GetCondition(p, ActionStop, Never{}).Met(p, event.Event{}, EventStats{}))
}

func TestGetCondition_MissingRestartUsesStart(t *testing.T) {
	wait := OnProcessUp{Source: event.PTP4l, ConfigName: ttestPtp4lConditions}
	p := &stubProc{conds: map[Action]Condition{ActionStart: wait}}
	assert.Equal(t, wait, GetCondition(p, ActionRestart, Immediate{}))
	assert.Equal(t, Never{}, GetCondition(p, ActionStop, Never{}))
}

func TestGetCondition_ExplicitRestartUsed(t *testing.T) {
	start := OnProcessUp{Source: event.PTP4l}
	restart := OnProcessUp{Source: event.TS2PHC}
	p := &stubProc{conds: map[Action]Condition{
		ActionStart:   start,
		ActionRestart: restart,
	}}
	assert.Equal(t, restart, GetCondition(p, ActionRestart, Immediate{}))
	assert.Equal(t, start, GetCondition(p, ActionStart, Immediate{}))
}

func TestGetCondition_NilProcessUsesFallback(t *testing.T) {
	assert.Equal(t, Immediate{}, GetCondition(nil, ActionStart, Immediate{}))
	assert.Equal(t, Never{}, GetCondition(nil, ActionStop, Never{}))
}

func TestOnProcessUp(t *testing.T) {
	p := &stubProc{}
	c := OnProcessUp{Source: event.PTP4l, ConfigName: ttestPtp4lConditions}
	assert.False(t, c.Met(p, event.Event{}, EventStats{}))
	up := event.ProcessStatusEvent(event.PTP4l, ttestPtp4lConditions, event.OC, "", 1)
	assert.True(t, c.Met(p, up, EventStats{}))
	down := event.ProcessStatusEvent(event.PTP4l, ttestPtp4lConditions, event.OC, "", 0)
	assert.False(t, c.Met(p, down, EventStats{}))
	wrongSrc := event.ProcessStatusEvent(event.GPSD, ttestPtp4lConditions, event.OC, "", 1)
	assert.False(t, c.Met(p, wrongSrc, EventStats{}))
	wrongCfg := event.ProcessStatusEvent(event.PTP4l, "other.config", event.OC, "", 1)
	assert.False(t, c.Met(p, wrongCfg, EventStats{}))
	reset := event.Event{Source: event.PTP4l, CfgName: ttestPtp4lConditions, Reset: true}
	assert.False(t, c.Met(p, reset, EventStats{}))
}

func TestAll(t *testing.T) {
	p := &stubProc{}
	c := All{Conditions: []Condition{
		OnProcessUp{Source: event.PTP4l},
		OnProcessUp{Source: event.GPSD},
	}}
	assert.False(t, c.Met(p, event.ProcessStatusEvent(event.PTP4l, "", event.OC, "", 1), EventStats{}))
	both := event.ProcessStatusEvent(event.PTP4l, "", event.OC, "", 1)
	// All requires every nested condition on the same event; a ptp4l Up is not gpsd Up.
	assert.False(t, c.Met(p, both, EventStats{}))
	empty := All{}
	assert.True(t, empty.Met(p, event.Event{}, EventStats{}))
}

func TestImmediateAlwaysTrue(t *testing.T) {
	p := &stubProc{}
	var c Immediate
	assert.True(t, c.Met(p, event.Event{}, EventStats{}))
	assert.True(t, c.Met(p, event.ProcessStatusEvent(event.PTP4l, "x", event.OC, "", 0), EventStats{}))
}

func TestNeverAlwaysFalse(t *testing.T) {
	p := &stubProc{}
	var c Never
	assert.False(t, c.Met(p, event.Event{}, EventStats{}))
	assert.False(t, c.Met(p, event.ProcessStatusEvent(event.PTP4l, "x", event.OC, "", 1), EventStats{}))
	assert.Equal(t, "Never", c.String())
}

func TestOnPluginEvent(t *testing.T) {
	p := &stubProc{}
	c := OnPluginEvent{EventName: "gnss_failover"}

	failover := event.PluginEvent("ntpfailover", "gnss_failover")
	assert.True(t, c.Met(p, failover, EventStats{}))

	recovered := event.PluginEvent("ntpfailover", "gnss_recovered")
	assert.False(t, c.Met(p, recovered, EventStats{}))

	// Non-PluginData event should not match
	processUp := event.ProcessStatusEvent(event.PTP4l, ttestPtp4lConditions, event.OC, "", 1)
	assert.False(t, c.Met(p, processUp, EventStats{}))

	// Empty event should not match
	assert.False(t, c.Met(p, event.Event{}, EventStats{}))
}

func TestOnProcessDown(t *testing.T) {
	p := &stubProc{}
	c := OnProcessDown{Source: event.PTP4l, ConfigName: ttestPtp4lConditions}

	down := event.ProcessStatusEvent(event.PTP4l, ttestPtp4lConditions, event.OC, "", 0)
	assert.True(t, c.Met(p, down, EventStats{}))

	up := event.ProcessStatusEvent(event.PTP4l, ttestPtp4lConditions, event.OC, "", 1)
	assert.False(t, c.Met(p, up, EventStats{}))

	wrongSrc := event.ProcessStatusEvent(event.GPSD, ttestPtp4lConditions, event.OC, "", 0)
	assert.False(t, c.Met(p, wrongSrc, EventStats{}))

	wrongCfg := event.ProcessStatusEvent(event.PTP4l, "other.config", event.OC, "", 0)
	assert.False(t, c.Met(p, wrongCfg, EventStats{}))

	// Non-ProcessStatus event should not match
	assert.False(t, c.Met(p, event.Event{}, EventStats{}))
}

func TestGetCondition_FallbackForNewActionTypes(t *testing.T) {
	p := &stubProc{}
	// Missing ActionEnable/ActionDisable should use the provided fallback
	assert.Equal(t, Never{}, GetCondition(p, ActionEnable, Never{}))
	assert.Equal(t, Never{}, GetCondition(p, ActionDisable, Never{}))

	// When explicitly configured, should return the configured condition
	p2 := &stubProc{conds: map[Action]Condition{
		ActionEnable: OnPluginEvent{EventName: "gnss_failover"},
	}}
	assert.Equal(t, OnPluginEvent{EventName: "gnss_failover"}, GetCondition(p2, ActionEnable, Never{}))
}

func TestAny(t *testing.T) {
	p := &stubProc{}
	c := Any{Conditions: []Condition{
		OnStateAndOffsetForCount{
			ClockID:    ttestTs2phcConditions,
			ConfigName: ttestTs2phcConditions,
			Source:     event.TS2PHC,
			State:      event.PTP_LOCKED,
			MaxOffset:  1e9,
			Count:      0,
		},
		OnPluginEvent{EventName: "gnss_recovered"},
	}}

	// LOCKED event with sub-second offset and a stats window satisfying count=0
	w := utils.NewWindow(10)
	w.Insert(500000.0)
	stats := EventStats{ttestTs2phcConditions: {event.TS2PHC: w}}

	locked := event.Event{
		Source:  event.TS2PHC,
		CfgName: ttestTs2phcConditions,
		Data:    &event.PTPData{State: event.PTP_LOCKED, Values: map[event.ValueType]interface{}{event.OFFSET: int64(500000)}},
	}
	assert.True(t, c.Met(p, locked, stats))

	// Plugin event satisfies second condition
	recovered := event.PluginEvent("ntpfailover", "gnss_recovered")
	assert.True(t, c.Met(p, recovered, EventStats{}))

	// Neither condition met
	wrongPlugin := event.PluginEvent("ntpfailover", "gnss_failover")
	assert.False(t, c.Met(p, wrongPlugin, EventStats{}))

	// Empty Any is false
	empty := Any{}
	assert.False(t, empty.Met(p, event.Event{}, EventStats{}))
}

func TestOnStateAndOffsetForCount(t *testing.T) {
	p := &stubProc{}
	c := OnStateAndOffsetForCount{
		ClockID:    ttestTs2phcConditions,
		ConfigName: ttestTs2phcConditions,
		Source:     event.TS2PHC,
		State:      event.PTP_LOCKED,
		MaxOffset:  1e9,
		Count:      1,
	}
	locked := func(offset int64) event.Event {
		return event.Event{
			Source:  event.TS2PHC,
			CfgName: ttestTs2phcConditions,
			Data:    &event.PTPData{State: event.PTP_LOCKED, Values: map[event.ValueType]interface{}{event.OFFSET: offset}},
		}
	}

	w := utils.NewWindow(10)
	w.Insert(500000.0)
	w.Insert(-400000.0)
	stats := EventStats{ttestTs2phcConditions: {event.TS2PHC: w}}
	assert.True(t, c.Met(p, locked(500000), stats), "two sub-second samples including negative should meet count")

	assert.False(t, c.Met(p, locked(500000), EventStats{}), "nil window should not match")

	emptyCfg := c
	emptyCfg.ConfigName = ""
	assert.False(t, emptyCfg.Met(p, locked(500000), stats), "empty ConfigName must not match")

	wrongSrc := locked(500000)
	wrongSrc.Source = event.PTP4l
	assert.False(t, c.Met(p, wrongSrc, stats), "wrong Source should not match")

	wLarge := utils.NewWindow(10)
	wLarge.Insert(-2000000000.0)
	wLarge.Insert(-2000000000.0)
	statsLarge := EventStats{ttestTs2phcConditions: {event.TS2PHC: wLarge}}
	assert.False(t, c.Met(p, locked(-2000000000), statsLarge), "abs(offset) above MaxOffset should not count")
}
