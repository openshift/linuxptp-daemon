package event

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

func TestGetLargestOffset_MultiDPLL_FaultyPhaseOffset(t *testing.T) {
	t.Parallel()
	cfgName := "ptp4l.0.config"
	leadingIFace := "ens1f0"

	e := &EventHandler{
		data:         map[string][]*Data{},
		clkSyncState: map[string]*clockSyncState{},
	}

	e.clkSyncState[cfgName] = &clockSyncState{
		state:        PTP_LOCKED,
		leadingIFace: leadingIFace,
	}

	now := time.Now().UnixMilli()
	dpllWindow := utils.NewWindow(WindowSize)
	for i := 0; i < WindowSize; i++ {
		dpllWindow.Insert(50) // leading DPLL at 50ns offset
	}

	dpllData := &Data{
		ProcessName: DPLL,
		window:      *dpllWindow,
		Details: DDetails{
			{
				IFace:  leadingIFace,
				State:  PTP_LOCKED,
				Offset: 50,
				time:   now,
			},
			{
				IFace:  "ens2f0",
				State:  PTP_LOCKED,
				Offset: FaultyPhaseOffset, // follower DPLL out of range
				time:   now,
			},
		},
	}
	e.data[cfgName] = []*Data{dpllData}

	got := e.getLargestOffset(cfgName)

	// The FaultyPhaseOffset in the follower's dd.Offset should be the worst
	if got != FaultyPhaseOffset {
		t.Errorf("getLargestOffset() = %d, want %d (FaultyPhaseOffset from follower DPLL)", got, FaultyPhaseOffset)
	}
}

func TestGetLargestOffset_MultiDPLL_AllConverged(t *testing.T) {
	t.Parallel()
	cfgName := "ptp4l.0.config"
	leadingIFace := "ens1f0"

	e := &EventHandler{
		data:         map[string][]*Data{},
		clkSyncState: map[string]*clockSyncState{},
	}

	e.clkSyncState[cfgName] = &clockSyncState{
		state:        PTP_LOCKED,
		leadingIFace: leadingIFace,
	}

	now := time.Now().UnixMilli()
	dpllWindow := utils.NewWindow(WindowSize)
	for i := 0; i < WindowSize; i++ {
		dpllWindow.Insert(30) // leading DPLL stable at 30ns
	}

	dpllData := &Data{
		ProcessName: DPLL,
		window:      *dpllWindow,
		Details: DDetails{
			{
				IFace:  leadingIFace,
				State:  PTP_LOCKED,
				Offset: 30,
				time:   now,
			},
			{
				IFace:  "ens2f0",
				State:  PTP_LOCKED,
				Offset: -80, // follower at -80ns (within range but larger abs)
				time:   now,
			},
		},
	}
	e.data[cfgName] = []*Data{dpllData}

	got := e.getLargestOffset(cfgName)

	// Leading uses window.Mean()=30, follower uses dd.Offset=-80
	// Worst = max(abs(30), abs(-80)) = -80
	if math.Abs(float64(got)) != 80 {
		t.Errorf("getLargestOffset() = %d, want ±80 (follower DPLL offset)", got)
	}
}

func TestGetLargestOffset_EmptyWindow_Skipped(t *testing.T) {
	t.Parallel()
	cfgName := "ptp4l.0.config"
	leadingIFace := "ens1f0"

	e := &EventHandler{
		data:         map[string][]*Data{},
		clkSyncState: map[string]*clockSyncState{},
	}

	e.clkSyncState[cfgName] = &clockSyncState{
		state:        PTP_LOCKED,
		leadingIFace: leadingIFace,
	}

	// PTP4l data with empty window (no offsets sent yet)
	ptp4lData := &Data{
		ProcessName: PTP4l,
		window:      *utils.NewWindow(WindowSize),
	}

	now := time.Now().UnixMilli()
	dpllWindow := utils.NewWindow(WindowSize)
	for i := 0; i < WindowSize; i++ {
		dpllWindow.Insert(25)
	}
	dpllData := &Data{
		ProcessName: DPLL,
		window:      *dpllWindow,
		Details: DDetails{
			{
				IFace:  leadingIFace,
				State:  PTP_LOCKED,
				Offset: 25,
				time:   now,
			},
		},
	}

	e.data[cfgName] = []*Data{ptp4lData, dpllData}

	got := e.getLargestOffset(cfgName)

	// PTP4l window is empty → skipped, DPLL window mean = 25
	if got != 25 {
		t.Errorf("getLargestOffset() = %d, want 25 (DPLL window mean, PTP4l skipped)", got)
	}
}

func TestAddEvent_FaultyPhaseOffset_NotInsertedInWindow(t *testing.T) {
	t.Parallel()
	d := &Data{
		ProcessName: DPLL,
		window:      *utils.NewWindow(WindowSize),
		Details: DDetails{
			{
				IFace:  "ens1f0",
				State:  PTP_LOCKED,
				Offset: 0,
				time:   0,
			},
		},
	}

	// Insert a valid offset first
	d.AddEvent(EventChannel{
		ProcessName: DPLL,
		IFace:       "ens1f0",
		State:       PTP_LOCKED,
		Time:        time.Now().UnixMilli(),
		Values:      map[ValueType]interface{}{OFFSET: int64(50)},
	})

	if d.window.IsEmpty() {
		t.Error("window should not be empty after valid offset insert")
	}

	// Insert FaultyPhaseOffset
	d.AddEvent(EventChannel{
		ProcessName: DPLL,
		IFace:       "ens1f0",
		State:       PTP_LOCKED,
		Time:        time.Now().UnixMilli() + 1,
		Values:      map[ValueType]interface{}{OFFSET: FaultyPhaseOffset},
	})

	// dd.Offset should have the sentinel
	if d.Details[0].Offset != FaultyPhaseOffset {
		t.Errorf("dd.Offset = %d, want FaultyPhaseOffset", d.Details[0].Offset)
	}

	// Window mean should still be 50 (sentinel was not inserted)
	got := d.window.Mean()
	if got != 50.0 {
		t.Errorf("window.Mean() = %f, want 50.0 (sentinel should not be in window)", got)
	}
}

func TestTBCSourceLossHoldoverPolicy(t *testing.T) {
	const cfgName = "ts2phc.1.config"
	const leadingIFace = "eno1np0"

	tests := []struct {
		name      string
		timeout   interface{}
		wantState PTPState
		wantClass fbprotocol.ClockClass
	}{
		{"disabled", uint64(0), PTP_FREERUN, protocol.ClockClassFreerun},
		{"enabled", uint64(30), PTP_HOLDOVER, fbprotocol.ClockClass(135)},
		{"unspecified", nil, PTP_HOLDOVER, fbprotocol.ClockClass(135)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &EventHandler{
				data:             map[string][]*Data{},
				clkSyncState:     map[string]*clockSyncState{},
				LeadingClockData: &LeadingClockParams{},
				downstreamCancel: map[string]context.CancelFunc{},
			}
			values := map[ValueType]interface{}{LeadingSource: true, ToFreeRunThreshold: uint64(500)}
			if tt.timeout != nil {
				values[LocalHoldoverTimeout] = tt.timeout
			}
			e.updateLeadingClockData(EventChannel{ProcessName: DPLL, IFace: leadingIFace, Values: values})

			// Upstream lost while the cached leading DPLL is still locked with a small offset.
			now := time.Now().UnixMilli()
			dpllWindow := utils.NewWindow(WindowSize)
			dpllWindow.Insert(10)
			e.clkSyncState[cfgName] = &clockSyncState{state: PTP_LOCKED, clockClass: 6, leadingIFace: leadingIFace}
			e.data[cfgName] = []*Data{
				{ProcessName: DPLL, State: PTP_LOCKED, window: *dpllWindow, Details: DDetails{{IFace: leadingIFace, State: PTP_LOCKED, time: now}}},
				{ProcessName: PTP4l, State: PTP_FREERUN, Details: DDetails{{IFace: "eno2np3", State: PTP_FREERUN, time: now}}},
			}

			e.Lock()
			got, _ := e.updateBCState(EventChannel{ProcessName: PTP4l, CfgName: cfgName, IFace: "eno2np3"})
			// The transition starts a downstream announce goroutine that waits for this lock;
			// removing the state makes it return before calling pmc or the leap file.
			delete(e.clkSyncState, cfgName)
			e.Unlock()

			if got.state != tt.wantState || got.clockClass != tt.wantClass {
				t.Fatalf("got state %s class %d, want state %s class %d", got.state, got.clockClass, tt.wantState, tt.wantClass)
			}
		})
	}
}

func TestDPLLLogLineIgnoresHoldoverTimeout(t *testing.T) {
	ev := EventChannel{ProcessName: DPLL, CfgName: "ts2phc.1.config", IFace: "eno1np0", State: PTP_LOCKED,
		Values: map[ValueType]interface{}{OFFSET: int64(0), LocalHoldoverTimeout: uint64(0)}}
	want := ev
	want.Values = map[ValueType]interface{}{OFFSET: int64(0)}
	got := strings.SplitN(ev.GetLogData(), "]:", 2)[1]
	base := strings.SplitN(want.GetLogData(), "]:", 2)[1]
	if got != base {
		t.Fatalf("timeout changed the DPLL log line: %q vs %q", got, base)
	}
}
