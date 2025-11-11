// # Assisted by watsonx Code Assistant
package event

import (
	"testing"
	"time"

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
		name      string
		cfgName   string
		data      map[string][]*Data
		syncState map[string]*clockSyncState
		threshold int
		expected  bool
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			expected: 100,
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
			expected: 25,
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
			expected: -30,
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
			expected: -25,
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
			expected: 20,
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
			expected: -40,
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
			expected: 0,
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
			expected: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := EventHandler{data: tt.data, clkSyncState: tt.clkSyncState}
			result := e.getLargestOffset(tt.cfgName)
			assert.Equal(t, tt.expected, result)
		})
	}
}
