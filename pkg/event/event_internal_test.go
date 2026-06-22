// # Assisted by watsonx Code Assistant
package event

import (
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
	Event Event
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

			Event: Event{
				Source: PTP4lProcessName,
				IFace:  "eth0",
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
			Event: Event{
				Source: PTP4lProcessName,
				IFace:  "ens5f0",
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
			Event: Event{
				Source: "otherProcess",
				IFace:  "eth0",
			},
		}

		mockEventHandler.Event = mockEventHandler.e.convergeConfig(mockEventHandler.Event)
		assert.Equal(t, "", mockEventHandler.Event.CfgName)
	})
}

const (
	testConfig = "config"
	testIface  = "iface"
)

func TestUpdateLeadingClockData_PTP4lProcessName(t *testing.T) {
	event := Event{
		Source: PTP4lProcessName,
		Data: &PTPData{
			Values: map[ValueType]interface{}{
				ControlledPortsConfig: testConfig,
				ClockIDKey:            "clockID",
			},
		},
	}

	expectedLeadingClockData := LeadingClockParams{
		controlledPortsConfig: testConfig,
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
	event := Event{
		Source: DPLL,
		IFace:  testIface,
		Data: &PTPData{
			Values: map[ValueType]interface{}{
				LeadingSource:            true,
				InSyncConditionThreshold: uint64(100),
				InSyncConditionTimes:     uint64(200),
				ToFreeRunThreshold:       uint64(300),
				MaxInSpecOffset:          uint64(400),
			},
		},
	}

	expectedLeadingClockData := LeadingClockParams{
		leadingInterface:         testIface,
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

func TestAddEvent_SourceLostPropagation(t *testing.T) {
	now := time.Now().UnixMilli()

	tests := []struct {
		name            string
		initialDetails  []*DataDetails
		event           Event
		expectedStates  map[string]PTPState
		expectedSrcLost map[string]bool
	}{
		{
			name: "source-lost propagates to stale LOCKED detail on different iface",
			initialDetails: []*DataDetails{
				{IFace: "eno8903", State: PTP_LOCKED, sourceLost: false, time: now - 5000},
				{IFace: "eno8703", State: PTP_LOCKED, sourceLost: false, time: now - 3000},
			},
			event: Event{
				Source: PTP4l,
				IFace:  "eno8703",
				Time:   now,
				Data:   &PTPData{State: PTP_FREERUN, SourceLost: true},
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
			event: Event{
				Source: PTP4l,
				IFace:  "eno8703",
				Time:   now,
				Data:   &PTPData{State: PTP_FREERUN, SourceLost: true},
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
			event: Event{
				Source: PTP4l,
				IFace:  "eno8703",
				Time:   now,
				Data:   &PTPData{State: PTP_LOCKED, SourceLost: false},
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
		ptp4lData.AddEvent(Event{
			Source: PTP4l,
			IFace:  "eno8703",
			Time:   now,
			Data:   &PTPData{State: PTP_FREERUN, SourceLost: true},
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

// fillDataWindows fills all Data windows in the given config with their
// first detail's offset value. This satisfies getLargestOffset's requirement
// that the window be full before it returns a real value.
func fillDataWindows(e *EventHandler, cfgName string, offset int64) { //nolint:unparam // cfgName kept for clarity
	for _, d := range e.data[cfgName] {
		d.window = *utils.NewWindow(WindowSize)
		for i := 0; i < WindowSize; i++ {
			d.window.Insert(float64(offset))
		}
	}
}

func TestUpdateGMState(t *testing.T) {
	const cfg = "ts2phc.0.config"
	const iface = "ens1f0"

	makeEvent := func(process EventSource, state PTPState, sourceLost bool) Event {
		e := Event{
			Source:     process,
			IFace:      iface,
			CfgName:    cfg,
			ClockType:  GM,
			Time:       time.Now().UnixMilli(),
			WriteToLog: true,
		}
		if process == GNSS {
			var gpsStatus int64
			if state == PTP_LOCKED {
				gpsStatus = 3
			}
			e.Data = &GNSSData{GPSStatus: gpsStatus, Offset: 0, SourceLost: sourceLost}
		} else {
			e.Data = &PTPData{
				State:      state,
				Values:     map[ValueType]interface{}{OFFSET: int64(0)},
				SourceLost: sourceLost,
			}
		}
		return e
	}

	type step struct {
		events         []Event
		outOfSpec      bool
		frequencyTrace bool
		wantState      PTPState
		wantClockClass fbprotocol.ClockClass
	}

	tests := []struct {
		desc  string
		steps []step
	}{
		{
			desc: "all sources locked",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
			},
		},
		{
			desc: "DPLL locked, GNSS locked, ts2phc freerun",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_FREERUN, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		{
			desc: "DPLL holdover",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_HOLDOVER, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
			},
		},
		{
			desc: "DPLL freerun",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_FREERUN, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
		{
			desc: "DPLL freerun after holdover out of spec",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_FREERUN, false),
					},
					outOfSpec:      true,
					frequencyTrace: true,
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassOutOfSpec,
				},
			},
		},
		{
			desc: "no DPLL yet, GNSS and ts2phc locked",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
			},
		},
		{
			desc: "GNSS sourceLost with ts2phc locked - stay with last state",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, true),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
			},
		},
		{
			desc: "DPLL locked, GNSS locked, ts2phc holdover",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_HOLDOVER, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
			},
		},
		{
			desc: "GNSS lost with ts2phc holdover",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, true),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_HOLDOVER, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
			},
		},
		{
			desc: "GNSS sourceLost waits for DPLL holdover then transitions to clockClass 7",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, true),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(DPLL, PTP_HOLDOVER, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
			},
		},
		{
			desc: "GNSS sourceLost then recovery restores clockClass 6",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, true),
						makeEvent(DPLL, PTP_HOLDOVER, false),
					},
					wantState:      PTP_HOLDOVER,
					wantClockClass: fbprotocol.ClockClass7,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
			},
		},
		{
			desc: "GNSS FREERUN without sourceLost goes to clockClass 248",
			steps: []step{
				{
					events: []Event{
						makeEvent(GNSS, PTP_LOCKED, false),
						makeEvent(DPLL, PTP_LOCKED, false),
						makeEvent(TS2PHCProcessName, PTP_LOCKED, false),
					},
					wantState:      PTP_LOCKED,
					wantClockClass: fbprotocol.ClockClass6,
				},
				{
					events: []Event{
						makeEvent(GNSS, PTP_FREERUN, false),
					},
					wantState:      PTP_FREERUN,
					wantClockClass: protocol.ClockClassFreerun,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			e := &EventHandler{
				data:             map[string][]*Data{},
				clkSyncState:     map[string]*clockSyncState{},
				LeadingClockData: newLeadingClockParams(),
			}

			for i, s := range tt.steps {
				for _, ev := range s.events {
					e.addEvent(ev)
				}
				e.outOfSpec = s.outOfSpec
				e.frequencyTraceable = s.frequencyTrace

				result := e.updateGMState(cfg)
				assert.Equal(t, s.wantState, result.state, "step %d: state", i)
				assert.Equal(t, s.wantClockClass, result.clockClass, "step %d: clockClass", i)
			}
		})
	}
}

func TestUpdateBCState(t *testing.T) {
	const cfg = "ptp4l.0.config"
	const iface = "ens1f0"

	makeBCEvent := func(process EventSource, state PTPState, offset int64, sourceLost bool) Event {
		return Event{
			Source:     process,
			IFace:      iface,
			CfgName:    cfg,
			ClockType:  BC,
			Time:       time.Now().UnixMilli(),
			WriteToLog: true,
			Data: &PTPData{
				State:      state,
				Values:     map[ValueType]interface{}{OFFSET: offset},
				SourceLost: sourceLost,
			},
		}
	}

	newBCHandler := func() *EventHandler {
		return &EventHandler{
			data:         map[string][]*Data{},
			clkSyncState: map[string]*clockSyncState{},
			LeadingClockData: &LeadingClockParams{
				leadingInterface:         iface,
				inSyncConditionThreshold: 100,
				inSyncConditionTimes:     1,
				toFreeRunThreshold:       1500,
				MaxInSpecOffset:          500,
				upstreamParentDataSet:    &protocol.ParentDataSet{},
				upstreamTimeProperties:   &protocol.TimePropertiesDS{},
				downstreamParentDataSet:  &protocol.ParentDataSet{},
				downstreamTimeProperties: &protocol.TimePropertiesDS{},
			},
		}
	}

	t.Run("FREERUN to LOCKED", func(t *testing.T) {
		e := newBCHandler()

		e.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillDataWindows(e, cfg, 10)

		result, needsTTSCAnnounce, needsDownstreamUpdate := e.updateBCState(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		assert.Equal(t, PTP_LOCKED, result.state, "should transition to LOCKED")
		assert.False(t, needsTTSCAnnounce, "not a TTSC")
		assert.False(t, needsDownstreamUpdate, "clockClass still uninitialized, no downstream update")
	})

	t.Run("LOCKED to FREERUN via offset", func(t *testing.T) {
		e := newBCHandler()

		// First establish LOCKED state
		e.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillDataWindows(e, cfg, 10)
		result, _, _ := e.updateBCState(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		assert.Equal(t, PTP_LOCKED, result.state, "setup: should be LOCKED")

		// Now send offset exceeding toFreeRunThreshold (1500)
		e.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 2000, false))
		result, needsTTSCAnnounce, needsDownstreamUpdate := e.updateBCState(makeBCEvent(DPLL, PTP_LOCKED, 2000, false))
		assert.Equal(t, PTP_FREERUN, result.state, "should transition to FREERUN")
		assert.Equal(t, protocol.ClockClassFreerun, result.clockClass, "clockClass should be 248")
		assert.False(t, needsTTSCAnnounce, "not a TTSC")
		assert.True(t, needsDownstreamUpdate, "FREERUN clockClass set, downstream needs update")
	})

	t.Run("LOCKED to HOLDOVER via source lost", func(t *testing.T) {
		e := newBCHandler()

		// Establish LOCKED state
		e.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillDataWindows(e, cfg, 10)
		result, _, _ := e.updateBCState(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		assert.Equal(t, PTP_LOCKED, result.state, "setup: should be LOCKED")

		// Source lost: PTP4l goes FREERUN, DPLL goes HOLDOVER
		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		e.addEvent(makeBCEvent(DPLL, PTP_HOLDOVER, 10, false))
		result, needsTTSCAnnounce, needsDownstreamUpdate := e.updateBCState(makeBCEvent(DPLL, PTP_HOLDOVER, 10, false))
		assert.Equal(t, PTP_HOLDOVER, result.state, "should transition to HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(135), result.clockClass, "clockClass should be 135 (holdover in-spec)")
		assert.False(t, needsTTSCAnnounce, "not a TTSC")
		assert.True(t, needsDownstreamUpdate, "holdover clockClass set, downstream needs update")
	})

	t.Run("HOLDOVER to LOCKED", func(t *testing.T) {
		e := newBCHandler()

		// Establish LOCKED then HOLDOVER
		e.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillDataWindows(e, cfg, 10)
		e.updateBCState(makeBCEvent(DPLL, PTP_LOCKED, 10, false))

		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		e.addEvent(makeBCEvent(DPLL, PTP_HOLDOVER, 10, false))
		result, _, _ := e.updateBCState(makeBCEvent(DPLL, PTP_HOLDOVER, 10, false))
		assert.Equal(t, PTP_HOLDOVER, result.state, "setup: should be HOLDOVER")

		// Restore: PTP4l and DPLL back to LOCKED
		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 5, false))
		e.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 5, false))
		fillDataWindows(e, cfg, 5)
		e.LeadingClockData.inSyncThresholdCounter = 0
		result, needsTTSCAnnounce, needsDownstreamUpdate := e.updateBCState(makeBCEvent(DPLL, PTP_LOCKED, 5, false))
		assert.Equal(t, PTP_LOCKED, result.state, "should transition back to LOCKED")
		assert.False(t, needsTTSCAnnounce, "not a TTSC")
		assert.True(t, needsDownstreamUpdate, "re-locked with non-uninitialized clockClass, downstream needs update")
	})

	t.Run("HOLDOVER to FREERUN via offset", func(t *testing.T) {
		e := newBCHandler()

		// Establish LOCKED then HOLDOVER
		e.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillDataWindows(e, cfg, 10)
		e.updateBCState(makeBCEvent(DPLL, PTP_LOCKED, 10, false))

		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		e.addEvent(makeBCEvent(DPLL, PTP_HOLDOVER, 10, false))
		result, _, _ := e.updateBCState(makeBCEvent(DPLL, PTP_HOLDOVER, 10, false))
		assert.Equal(t, PTP_HOLDOVER, result.state, "setup: should be HOLDOVER")

		// Offset exceeds toFreeRunThreshold
		e.addEvent(makeBCEvent(DPLL, PTP_HOLDOVER, 2000, false))
		result, needsTTSCAnnounce, needsDownstreamUpdate := e.updateBCState(makeBCEvent(DPLL, PTP_HOLDOVER, 2000, false))
		assert.Equal(t, PTP_FREERUN, result.state, "should transition to FREERUN")
		assert.Equal(t, protocol.ClockClassFreerun, result.clockClass, "clockClass should be 248")
		assert.False(t, needsTTSCAnnounce, "not a TTSC")
		assert.True(t, needsDownstreamUpdate, "FREERUN clockClass set, downstream needs update")
	})

	t.Run("HOLDOVER in-spec to out-of-spec", func(t *testing.T) {
		e := newBCHandler()

		// Establish LOCKED then HOLDOVER
		e.addEvent(makeBCEvent(DPLL, PTP_LOCKED, 10, false))
		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_LOCKED, 10, false))
		fillDataWindows(e, cfg, 10)
		e.updateBCState(makeBCEvent(DPLL, PTP_LOCKED, 10, false))

		e.addEvent(makeBCEvent(PTP4lProcessName, PTP_FREERUN, 10, true))
		e.addEvent(makeBCEvent(DPLL, PTP_HOLDOVER, 10, false))
		result, _, _ := e.updateBCState(makeBCEvent(DPLL, PTP_HOLDOVER, 10, false))
		assert.Equal(t, PTP_HOLDOVER, result.state, "setup: should be HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(135), result.clockClass, "setup: should be in-spec (135)")

		// Offset exceeds MaxInSpecOffset (500) but stays below toFreeRunThreshold (1500)
		e.addEvent(makeBCEvent(DPLL, PTP_HOLDOVER, 600, false))
		fillDataWindows(e, cfg, 600)
		result, needsTTSCAnnounce, needsDownstreamUpdate := e.updateBCState(makeBCEvent(DPLL, PTP_HOLDOVER, 600, false))
		assert.Equal(t, PTP_HOLDOVER, result.state, "should stay in HOLDOVER")
		assert.Equal(t, fbprotocol.ClockClass(165), result.clockClass, "clockClass should change to 165 (out-of-spec)")
		assert.False(t, needsTTSCAnnounce, "not a TTSC")
		assert.True(t, needsDownstreamUpdate, "clockClass changed, downstream needs update")
	})
}

func TestUpdateSpecState(t *testing.T) {
	newHandler := func() *EventHandler {
		return &EventHandler{
			data:         map[string][]*Data{},
			clkSyncState: map[string]*clockSyncState{},
		}
	}

	t.Run("DPLL event sets outOfSpec", func(t *testing.T) {
		e := newHandler()
		assert.False(t, e.outOfSpec, "initial outOfSpec should be false")

		e.updateSpecState(Event{Source: DPLL, Data: &PTPData{OutOfSpec: true}})
		assert.True(t, e.outOfSpec, "DPLL event with OutOfSpec=true should set outOfSpec")

		e.updateSpecState(Event{Source: DPLL, Data: &PTPData{OutOfSpec: false}})
		assert.False(t, e.outOfSpec, "DPLL event with OutOfSpec=false should clear outOfSpec")
	})

	t.Run("DPLL event sets frequencyTraceable", func(t *testing.T) {
		e := newHandler()
		assert.False(t, e.frequencyTraceable, "initial frequencyTraceable should be false")

		e.updateSpecState(Event{Source: DPLL, Data: &PTPData{FrequencyTraceable: true}})
		assert.True(t, e.frequencyTraceable, "DPLL event should set frequencyTraceable")

		e.updateSpecState(Event{Source: DPLL, Data: &PTPData{FrequencyTraceable: false}})
		assert.False(t, e.frequencyTraceable, "DPLL event should clear frequencyTraceable")
	})

	t.Run("non-DPLL event does not change spec state", func(t *testing.T) {
		e := newHandler()

		e.updateSpecState(Event{Source: DPLL, Data: &PTPData{OutOfSpec: true, FrequencyTraceable: true}})
		assert.True(t, e.outOfSpec)
		assert.True(t, e.frequencyTraceable)

		e.updateSpecState(Event{Source: TS2PHC, Data: &PTPData{OutOfSpec: false, FrequencyTraceable: false}})
		assert.True(t, e.outOfSpec, "non-DPLL event should not change outOfSpec")
		assert.True(t, e.frequencyTraceable, "non-DPLL event should not change frequencyTraceable")
	})
}
