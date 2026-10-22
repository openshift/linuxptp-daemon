// # Assisted by watsonx Code Assistant
package event

import (
	"context"
	"strings"
	"testing"
	"time"

	fbprotocol "github.com/facebook/time/ptp/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
)

type MockData struct {
	Data map[string][]*Data
}

type MockEventHandler struct {
	Event EventChannel
	e     EventHandler
}

func TestConvergeConfig(t *testing.T) {
	t.Run("PTP4lProcessName with matching IFace", func(t *testing.T) {
		mockData := &MockData{
			Data: map[string][]*Data{
				"config1": {
					{ProcessName: DPLL, Details: DDetails{{IFace: "eth0"}}},
				},
			},
		}
		mockEventHandler := &MockEventHandler{
			e: EventHandler{
				data: mockData.Data,
			},

			Event: EventChannel{
				ProcessName: PTP4lProcessName,
				IFace:       "eth0",
			},
		}
		mockEventHandler.Event = mockEventHandler.e.convergeConfig(mockEventHandler.Event)
		assert.Equal(t, "config1", mockEventHandler.Event.CfgName)
	})

	t.Run("PTP4lProcessName without matching IFace", func(t *testing.T) {
		mockData := &MockData{
			Data: map[string][]*Data{
				"config1": {
					{ProcessName: DPLL, Details: DDetails{{IFace: "ens4f1"}}},
				},
			},
		}
		mockEventHandler := &MockEventHandler{
			e: EventHandler{
				data: mockData.Data,
			},
			Event: EventChannel{
				ProcessName: PTP4lProcessName,
				IFace:       "ens5f0",
			},
		}

		mockEventHandler.Event = mockEventHandler.e.convergeConfig(mockEventHandler.Event)
		assert.Equal(t, "", mockEventHandler.Event.CfgName)
	})

	t.Run("Non-PTP4lProcessName", func(t *testing.T) {
		mockData := &MockData{
			Data: map[string][]*Data{
				"config1": {
					{ProcessName: DPLL, Details: DDetails{{IFace: "eth0"}}},
				},
			},
		}
		mockEventHandler := &MockEventHandler{
			e: EventHandler{
				data: mockData.Data,
			},
			Event: EventChannel{
				ProcessName: "otherProcess",
				IFace:       "eth0",
			},
		}

		mockEventHandler.Event = mockEventHandler.e.convergeConfig(mockEventHandler.Event)
		assert.Equal(t, "", mockEventHandler.Event.CfgName)
	})
}

const (
	testConfig     = "config"
	testIface      = "iface"
	testLeadingNIC = "ens2f0"
)

func TestUpdateLeadingClockData_PTP4lProcessName(t *testing.T) {
	event := EventChannel{
		ProcessName: PTP4lProcessName,
		Values: map[ValueType]interface{}{
			ControlledPortsConfig: "config",
			ClockIDKey:            "clockID",
		},
	}

	expectedLeadingClockData := LeadingClockParams{
		controlledPortsConfig: "config",
		clockID:               "clockID",
	}

	e := EventHandler{
		LeadingClockData: &LeadingClockParams{},
	}
	e.updateLeadingClockData(event)

	assert.Equal(t, expectedLeadingClockData.controlledPortsConfig, e.LeadingClockData.controlledPortsConfig)
	assert.Equal(t, expectedLeadingClockData.clockID, e.LeadingClockData.clockID)
}

func TestUpdateLeadingClockData_DPLL(t *testing.T) {
	event := EventChannel{
		ProcessName: DPLL,
		IFace:       "iface",
		Values: map[ValueType]interface{}{
			LeadingSource:            true,
			InSyncConditionThreshold: uint64(100),
			InSyncConditionTimes:     uint64(200),
			ToFreeRunThreshold:       uint64(300),
			MaxInSpecOffset:          uint64(400),
		},
	}

	expectedLeadingClockData := LeadingClockParams{
		leadingInterface:         "iface",
		inSyncConditionThreshold: 100,
		inSyncConditionTimes:     200,
		toFreeRunThreshold:       300,
		MaxInSpecOffset:          400,
	}

	e := EventHandler{
		LeadingClockData: &LeadingClockParams{},
	}
	e.updateLeadingClockData(event)

	assert.Equal(t, expectedLeadingClockData.leadingInterface, e.LeadingClockData.leadingInterface)
	assert.Equal(t, expectedLeadingClockData.inSyncConditionThreshold, e.LeadingClockData.inSyncConditionThreshold)
	assert.Equal(t, expectedLeadingClockData.inSyncConditionTimes, e.LeadingClockData.inSyncConditionTimes)
	assert.Equal(t, expectedLeadingClockData.toFreeRunThreshold, e.LeadingClockData.toFreeRunThreshold)
	assert.Equal(t, expectedLeadingClockData.MaxInSpecOffset, e.LeadingClockData.MaxInSpecOffset)
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
				LeadingClockData: newLeadingClockParams(),
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

func TestGetLeadingInterfaceBC(t *testing.T) {
	tests := []struct {
		name     string
		input    *EventHandler
		expected string
	}{
		{
			name: "LeadingInterface is not empty",
			input: &EventHandler{
				LeadingClockData: &LeadingClockParams{
					leadingInterface: "eth0",
				},
			},
			expected: "eth0",
		},
		{
			name: "LeadingInterface is empty",
			input: &EventHandler{
				LeadingClockData: &LeadingClockParams{},
			},
			expected: LEADING_INTERFACE_UNKNOWN,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.input.getLeadingInterfaceBC()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestInSpecCondition(t *testing.T) {
	t.Run("returns false when MaxInSpecOffset is 0", func(t *testing.T) {
		e := EventHandler{
			LeadingClockData: &LeadingClockParams{MaxInSpecOffset: 0},
		}
		result := e.inSpecCondition("testConfig")
		assert.False(t, result)
	})

	t.Run("returns false when offset is out of spec", func(t *testing.T) {
		e := EventHandler{
			data: map[string][]*Data{
				"testConfig": {
					{ProcessName: DPLL, Details: []*DataDetails{{IFace: "iface1", Offset: 10}}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"testConfig": {leadingIFace: "iface1"},
			},
			LeadingClockData: &LeadingClockParams{MaxInSpecOffset: 5},
		}
		result := e.inSpecCondition("testConfig")
		assert.False(t, result)
	})

	t.Run("returns true when offset is in spec", func(t *testing.T) {
		e := EventHandler{
			data: map[string][]*Data{
				"testConfig": {
					{ProcessName: DPLL, Details: []*DataDetails{{IFace: "iface1", Offset: 3}}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"testConfig": {leadingIFace: "iface1"},
			},
			LeadingClockData: &LeadingClockParams{MaxInSpecOffset: 5},
		}
		result := e.inSpecCondition("testConfig")
		assert.True(t, result)
	})
}

func TestFreeRunCondition(t *testing.T) {
	tests := []struct {
		name       string
		cfgName    string
		data       map[string][]*Data
		syncState  map[string]*clockSyncState
		threshold  int
		fillWindow bool
		expected   bool
	}{
		{
			name:      "Free run condition not met",
			cfgName:   "test",
			data:      map[string][]*Data{"test": {{ProcessName: DPLL, Details: []*DataDetails{{IFace: "IFace1", Offset: 5}}}}},
			syncState: map[string]*clockSyncState{"test": {leadingIFace: "IFace1"}},
			threshold: 10,
			expected:  false,
		},
		{
			name:      "Free run condition met",
			cfgName:   "test",
			data:      map[string][]*Data{"test": {{ProcessName: DPLL, Details: []*DataDetails{{IFace: "IFace1", Offset: 15}}}}},
			syncState: map[string]*clockSyncState{"test": {leadingIFace: "IFace1"}},
			threshold: 10,
			expected:  true,
		},
		{
			name:      "Free run condition pending initialization",
			cfgName:   "test",
			data:      map[string][]*Data{},
			syncState: map[string]*clockSyncState{"test": {leadingIFace: "IFace1"}},
			threshold: 0,
			expected:  true,
		},
		{
			name:    "Free run condition met via PTP4l window mean",
			cfgName: "test",
			data: map[string][]*Data{"test": {
				{ProcessName: DPLL, Details: []*DataDetails{{IFace: "IFace1", Offset: 5}}},
				{ProcessName: PTP4l, Details: []*DataDetails{{IFace: "IFace1", Offset: 9798319463}}},
			}},
			syncState:  map[string]*clockSyncState{"test": {leadingIFace: "IFace1"}},
			threshold:  1500,
			fillWindow: true,
			expected:   true,
		},
		{
			name:    "PTP4l window mean below threshold does not trigger free run",
			cfgName: "test",
			data: map[string][]*Data{"test": {
				{ProcessName: DPLL, Details: []*DataDetails{{IFace: "IFace1", Offset: 5}}},
				{ProcessName: PTP4l, Details: []*DataDetails{{IFace: "IFace1", Offset: 100}}},
			}},
			syncState:  map[string]*clockSyncState{"test": {leadingIFace: "IFace1"}},
			threshold:  1500,
			fillWindow: true,
			expected:   false,
		},
		{
			name:    "PTP4l with empty window is skipped",
			cfgName: "test",
			data: map[string][]*Data{"test": {
				{ProcessName: DPLL, Details: []*DataDetails{{IFace: "IFace1", Offset: 5}}},
				{ProcessName: PTP4l, Details: []*DataDetails{{IFace: "IFace1", Offset: 9798319463}}},
			}},
			syncState: map[string]*clockSyncState{"test": {leadingIFace: "IFace1"}},
			threshold: 1500,
			expected:  false,
		},
		{
			name:    "PTP4l stale detail on leading iface does not trigger free run when window has good data",
			cfgName: "test",
			data: map[string][]*Data{"test": {
				{ProcessName: DPLL, Details: []*DataDetails{{IFace: "eno8703", Offset: 0}}},
				{ProcessName: PTP4l, Details: []*DataDetails{
					{IFace: "eno8703", Offset: -93},
					{IFace: "eno8903", Offset: 2},
				}},
			}},
			syncState:  map[string]*clockSyncState{"test": {leadingIFace: "eno8703"}},
			threshold:  30,
			fillWindow: true,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fillWindow {
				for _, processData := range tt.data {
					for _, d := range processData {
						if d.ProcessName == PTP4l && len(d.Details) > 0 {
							d.window = *utils.NewWindow(WindowSize)
							// Fill with the last detail's offset to simulate the active port feeding the window
							lastOffset := d.Details[len(d.Details)-1].Offset
							for i := 0; i < WindowSize; i++ {
								d.window.Insert(float64(lastOffset))
							}
						}
					}
				}
			}
			e := &EventHandler{
				data:             tt.data,
				clkSyncState:     tt.syncState,
				LeadingClockData: &LeadingClockParams{toFreeRunThreshold: tt.threshold},
			}

			result := e.freeRunCondition(tt.cfgName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetLargestOffset(t *testing.T) {
	currentTime := time.Now().Unix()
	staleTime := (currentTime - StaleEventAfter) * 1000
	recentTime := currentTime * 1000

	tests := []struct {
		name         string
		cfgName      string
		data         map[string][]*Data
		clkSyncState map[string]*clockSyncState
		fillWindow   bool
		expected     int64
	}{
		{
			name:     "No data for config",
			cfgName:  "nonexistent",
			data:     map[string][]*Data{},
			expected: FaultyPhaseOffset,
		},
		{
			name:    "No process data",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {},
			},
			expected: FaultyPhaseOffset,
		},
		{
			name:    "No details in data",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{}},
					{ProcessName: "other", Details: []*DataDetails{}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			expected: FaultyPhaseOffset,
		},
		{
			name:    "Window not full - should return FaultyPhaseOffset",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{
						{IFace: "eth0", Offset: 15, time: recentTime},
						{IFace: "eth1", Offset: 5, time: recentTime},
					}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			expected: FaultyPhaseOffset,
		},
		{
			name:    "Single offset value",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{
						{IFace: "eth0", Offset: 100, time: recentTime},
					}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			fillWindow: true,
			expected:   100,
		},
		{
			name:    "Multiple offsets - largest positive",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{
						{IFace: "eth0", Offset: 15, time: recentTime},
						{IFace: "eth1", Offset: 5, time: recentTime},
						{IFace: "eth2", Offset: 25, time: recentTime},
					}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			fillWindow: true,
			expected:   25,
		},
		{
			name:    "Multiple offsets - largest negative",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{
						{IFace: "eth0", Offset: -30, time: recentTime},
						{IFace: "eth1", Offset: 5, time: recentTime},
						{IFace: "eth2", Offset: -10, time: recentTime},
					}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			fillWindow: true,
			expected:   -30,
		},
		{
			name:    "Mixed positive and negative - largest absolute value",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{
						{IFace: "eth0", Offset: 20, time: recentTime},
						{IFace: "eth1", Offset: -25, time: recentTime},
						{IFace: "eth2", Offset: 15, time: recentTime},
					}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			fillWindow: true,
			expected:   -25,
		},
		{
			name:    "Stale data filtered out",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{
						{IFace: "eth0", Offset: 100, time: staleTime - 1000}, // stale
						{IFace: "eth1", Offset: 20, time: recentTime},        // recent
						{IFace: "eth2", Offset: 50, time: staleTime - 500},   // stale
					}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			fillWindow: true,
			expected:   20,
		},
		{
			name:    "All data stale - should return FaultyPhaseOffset",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{
						{IFace: "eth0", Offset: 15, time: staleTime - 1000}, // stale, ignored
						{IFace: "eth1", Offset: 50, time: staleTime - 500},  // stale, ignored
						{IFace: "eth2", Offset: 30, time: staleTime - 200},  // stale, ignored
					}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			expected: FaultyPhaseOffset,
		},
		{
			name:    "Multiple processes with different offsets",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{
						{IFace: "eth0", Offset: 20, time: recentTime},
					}},
					{ProcessName: "ptp4l", Details: []*DataDetails{
						{IFace: "eth1", Offset: 35, time: recentTime},
					}},
					{ProcessName: "ts2phc", Details: []*DataDetails{
						{IFace: "eth2", Offset: -40, time: recentTime},
					}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			fillWindow: true,
			expected:   -40,
		},
		{
			name:    "Zero offset values",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{
						{IFace: "eth0", Offset: 0, time: recentTime},
						{IFace: "eth1", Offset: 0, time: recentTime},
					}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			fillWindow: true,
			expected:   0,
		},
		{
			name:    "Mix of zero and non-zero offsets",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL, Details: []*DataDetails{
						{IFace: "eth0", Offset: 0, time: recentTime},
						{IFace: "eth1", Offset: 10, time: recentTime},
						{IFace: "eth2", Offset: 0, time: recentTime},
					}},
				},
			},
			clkSyncState: map[string]*clockSyncState{
				"test": {
					leadingIFace: "eth99",
				},
			},
			fillWindow: true,
			expected:   10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fillWindow {
				for _, processData := range tt.data {
					for _, d := range processData {
						if len(d.Details) == 0 {
							continue
						}
						d.window = *utils.NewWindow(WindowSize)
						for i := 0; i < WindowSize; i++ {
							offset := d.Details[i%len(d.Details)].Offset
							d.window.Insert(float64(offset))
						}
					}
				}
			}
			e := EventHandler{data: tt.data, clkSyncState: tt.clkSyncState}
			result := e.getLargestOffset(tt.cfgName)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetLargestOffset_EmptyPTP4lWindowSkipped(t *testing.T) {
	recentTime := time.Now().UnixMilli()

	dpllData := &Data{
		ProcessName: DPLL,
		Details:     []*DataDetails{{IFace: "eth0", Offset: 20, time: recentTime}},
		window:      *utils.NewWindow(WindowSize),
	}
	for i := 0; i < WindowSize; i++ {
		dpllData.window.Insert(20)
	}

	ptp4lData := &Data{
		ProcessName: PTP4l,
		Details:     []*DataDetails{{IFace: "eth0", Offset: 9798319463, time: recentTime}},
		window:      *utils.NewWindow(WindowSize),
	}
	// PTP4l window left empty — simulates state-change-only events (no OFFSET)

	e := EventHandler{
		data: map[string][]*Data{
			"test": {dpllData, ptp4lData},
		},
		clkSyncState: map[string]*clockSyncState{
			"test": {leadingIFace: "eth99"},
		},
	}

	result := e.getLargestOffset("test")
	assert.Equal(t, int64(20), result, "should use DPLL data, skipping PTP4l with empty window")
}

func TestGetLargestOffset_PartiallyFilledWindowBlocksResult(t *testing.T) {
	recentTime := time.Now().UnixMilli()

	dpllData := &Data{
		ProcessName: DPLL,
		Details:     []*DataDetails{{IFace: "eth0", Offset: 20, time: recentTime}},
		window:      *utils.NewWindow(WindowSize),
	}
	dpllData.window.Insert(20) // partially filled (1 of WindowSize)

	e := EventHandler{
		data: map[string][]*Data{
			"test": {dpllData},
		},
		clkSyncState: map[string]*clockSyncState{
			"test": {leadingIFace: "eth99"},
		},
	}

	result := e.getLargestOffset("test")
	assert.Equal(t, FaultyPhaseOffset, result, "partially filled window should return FaultyPhaseOffset")
}

func TestGetLargestOffset_PartialTs2phcWindowDoesNotBlock(t *testing.T) {
	recentTime := time.Now().UnixMilli()

	dpllData := &Data{
		ProcessName: DPLL,
		Details:     []*DataDetails{{IFace: testLeadingNIC, Offset: -10, time: recentTime}},
		window:      *utils.NewWindow(WindowSize),
	}
	for i := 0; i < WindowSize; i++ {
		dpllData.window.Insert(-10)
	}

	// Simulate T-BC locked path: ts2phc has only a few samples (leading NIC
	// does not produce steady ts2phc offsets outside holdover).
	ts2phcData := &Data{
		ProcessName: TS2PHCProcessName,
		Details:     []*DataDetails{{IFace: "ens2f1", Offset: 5, time: recentTime}},
		window:      *utils.NewWindow(WindowSize),
	}
	ts2phcData.window.Insert(5)

	e := EventHandler{
		data: map[string][]*Data{
			"test": {dpllData, ts2phcData},
		},
		clkSyncState: map[string]*clockSyncState{
			"test": {leadingIFace: testLeadingNIC},
		},
	}

	result := e.getLargestOffset("test")
	assert.NotEqual(t, FaultyPhaseOffset, result,
		"partial ts2phc window must not return FaultyPhaseOffset")
	assert.Equal(t, int64(-10), result,
		"worst abs offset remains DPLL when |ts2phc| is smaller")
}

func TestGetLargestOffset_Ts2phcWithoutWindowUsesRawOffset(t *testing.T) {
	recentTime := time.Now().UnixMilli()

	ts2phcData := &Data{
		ProcessName: TS2PHCProcessName,
		Details:     []*DataDetails{{IFace: testLeadingNIC, Offset: -40, time: recentTime}},
		window:      *utils.NewWindow(WindowSize),
	}
	// window left empty — ts2phc must still contribute via raw detail offset

	e := EventHandler{
		data: map[string][]*Data{
			"test": {ts2phcData},
		},
		clkSyncState: map[string]*clockSyncState{
			"test": {leadingIFace: testLeadingNIC},
		},
	}

	result := e.getLargestOffset("test")
	assert.Equal(t, int64(-40), result, "ts2phc offsets are not window-filtered")
}

func TestAddEvent_SourceLostPropagation(t *testing.T) {
	StateRegisterer = NewStateNotifier()
	now := time.Now().UnixMilli()

	tests := []struct {
		name            string
		initialDetails  []*DataDetails
		event           EventChannel
		expectedStates  map[string]PTPState
		expectedSrcLost map[string]bool
	}{
		{
			name: "source-lost propagates to stale LOCKED detail on different iface",
			initialDetails: []*DataDetails{
				{IFace: "eno8903", State: PTP_LOCKED, sourceLost: false, time: now - 5000},
				{IFace: "eno8703", State: PTP_LOCKED, sourceLost: false, time: now - 3000},
			},
			event: EventChannel{
				ProcessName: PTP4l,
				IFace:       "eno8703",
				State:       PTP_FREERUN,
				SourceLost:  true,
				Time:        now,
			},
			expectedStates: map[string]PTPState{
				"eno8903": PTP_FREERUN,
				"eno8703": PTP_FREERUN,
			},
			expectedSrcLost: map[string]bool{
				"eno8903": true,
				"eno8703": true,
			},
		},
		{
			name: "source-lost does not overwrite already-FREERUN detail",
			initialDetails: []*DataDetails{
				{IFace: "eno8903", State: PTP_FREERUN, sourceLost: true, time: now - 2000},
				{IFace: "eno8703", State: PTP_LOCKED, sourceLost: false, time: now - 1000},
			},
			event: EventChannel{
				ProcessName: PTP4l,
				IFace:       "eno8703",
				State:       PTP_FREERUN,
				SourceLost:  true,
				Time:        now,
			},
			expectedStates: map[string]PTPState{
				"eno8903": PTP_FREERUN,
				"eno8703": PTP_FREERUN,
			},
			expectedSrcLost: map[string]bool{
				"eno8903": true,
				"eno8703": true,
			},
		},
		{
			name: "non-source-lost event does not propagate",
			initialDetails: []*DataDetails{
				{IFace: "eno8903", State: PTP_LOCKED, sourceLost: false, time: now - 5000},
				{IFace: "eno8703", State: PTP_FREERUN, sourceLost: false, time: now - 3000},
			},
			event: EventChannel{
				ProcessName: PTP4l,
				IFace:       "eno8703",
				State:       PTP_LOCKED,
				SourceLost:  false,
				Time:        now,
			},
			expectedStates: map[string]PTPState{
				"eno8903": PTP_LOCKED,
				"eno8703": PTP_LOCKED,
			},
			expectedSrcLost: map[string]bool{
				"eno8903": false,
				"eno8703": false,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &Data{
				ProcessName: PTP4l,
				Details:     tt.initialDetails,
			}
			d.AddEvent(tt.event)
			for _, dd := range d.Details {
				assert.Equal(t, tt.expectedStates[dd.IFace], dd.State, "state for %s", dd.IFace)
				assert.Equal(t, tt.expectedSrcLost[dd.IFace], dd.sourceLost, "sourceLost for %s", dd.IFace)
			}
		})
	}
}

func TestIsSourceLostBC_StaleDetailFixed(t *testing.T) {
	now := time.Now().UnixMilli()

	t.Run("stale LOCKED detail no longer fools isSourceLostBC after source-lost propagation", func(t *testing.T) {
		ptp4lData := &Data{
			ProcessName: PTP4l,
			Details: []*DataDetails{
				{IFace: "eno8903", State: PTP_LOCKED, sourceLost: false, time: now - 5000},
				{IFace: "eno8703", State: PTP_LOCKED, sourceLost: false, time: now - 3000},
			},
		}
		dpllData := &Data{
			ProcessName: DPLL,
			Details: []*DataDetails{
				{IFace: "eno8703", State: PTP_LOCKED, time: now},
			},
		}

		e := &EventHandler{
			data: map[string][]*Data{
				"cfg": {ptp4lData, dpllData},
			},
		}

		// Before source-lost event: PTP source should NOT be lost
		assert.False(t, e.isSourceLostBC("cfg"), "before source-lost event, ptpLost should be false")

		// Simulate source-lost event on eno8703 (active port fallback)
		ptp4lData.AddEvent(EventChannel{
			ProcessName: PTP4l,
			IFace:       "eno8703",
			State:       PTP_FREERUN,
			SourceLost:  true,
			Time:        now,
		})

		// After source-lost propagation: PTP source should be lost
		assert.True(t, e.isSourceLostBC("cfg"), "after source-lost propagation, ptpLost should be true")
	})

	t.Run("single LOCKED detail keeps source as not lost", func(t *testing.T) {
		ptp4lData := &Data{
			ProcessName: PTP4l,
			Details: []*DataDetails{
				{IFace: "eno8903", State: PTP_FREERUN, sourceLost: true, time: now},
				{IFace: "eno8703", State: PTP_LOCKED, sourceLost: false, time: now},
			},
		}
		dpllData := &Data{
			ProcessName: DPLL,
			Details: []*DataDetails{
				{IFace: "eno8703", State: PTP_LOCKED, time: now},
			},
		}
		e := &EventHandler{
			data: map[string][]*Data{
				"cfg": {ptp4lData, dpllData},
			},
		}
		assert.False(t, e.isSourceLostBC("cfg"))
	})
}
