// # Assisted by watsonx Code Assistant
package event

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/protocol"
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
					{ProcessName: DPLL, Details: DDetails{{IFace: "eth1"}}},
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
			TimePropertiesDataSet: &protocol.TimePropertiesDS{},
			ControlledPortsConfig: "config",
			ParentDataSet:         &protocol.ParentDataSet{},
			CurrentDataSet:        &protocol.CurrentDS{StepsRemoved: 10},
		},
	}

	expectedLeadingClockData := LeadingClockParams{
		upstreamTimeProperties:        event.Values[TimePropertiesDataSet].(*protocol.TimePropertiesDS),
		controlledPortsConfig:         "config",
		upstreamParentDataSet:         event.Values[ParentDataSet].(*protocol.ParentDataSet),
		upstreamCurrentDSStepsRemoved: 10,
	}

	e := EventHandler{
		LeadingClockData: &LeadingClockParams{
			upstreamTimeProperties: &protocol.TimePropertiesDS{},
			upstreamParentDataSet:  &protocol.ParentDataSet{},
		},
	}
	e.updateLeadingClockData(event)

	assert.Equal(t, expectedLeadingClockData.upstreamParentDataSet, e.LeadingClockData.upstreamParentDataSet)
	assert.Equal(t, expectedLeadingClockData.upstreamTimeProperties, e.LeadingClockData.upstreamTimeProperties)
	assert.Equal(t, expectedLeadingClockData.controlledPortsConfig, e.LeadingClockData.controlledPortsConfig)
	assert.Equal(t, expectedLeadingClockData.upstreamCurrentDSStepsRemoved, e.LeadingClockData.upstreamCurrentDSStepsRemoved)
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

type testCase struct {
	name     string
	cfgName  string
	data     map[string][]*Data
	expected int64
}

func TestGetLargestOffset(t *testing.T) {
	// Test cases
	testCases := []testCase{
		{
			name:    "No DPLL data",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: "other"},
					{ProcessName: "other2"},
				},
			},
			expected: FaultyPhaseOffset,
		},
		{
			name:    "DPLL data with no offset",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {
					{ProcessName: DPLL},
					{ProcessName: "other"},
				},
			},
			expected: FaultyPhaseOffset,
		},
		{
			name:    "DPLL data with positive offset",
			cfgName: "test",
			data: map[string][]*Data{"test": {{ProcessName: DPLL, Details: []*DataDetails{
				{IFace: "IFace1", Offset: 15},
				{IFace: "IFace2", Offset: 5},
			}}}},
			expected: 15,
		},
		{
			name:    "DPLL data with positive offset",
			cfgName: "test",
			data: map[string][]*Data{
				"test": {{ProcessName: DPLL, Details: []*DataDetails{
					{IFace: "IFace1", Offset: -15},
					{IFace: "IFace2", Offset: 5},
				}},
					{ProcessName: "other"},
				}},

			expected: -15,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			e := EventHandler{data: tc.data}
			result := e.getLargestOffset(tc.cfgName)
			assert.Equal(t, tc.expected, result)
		})
	}
}
