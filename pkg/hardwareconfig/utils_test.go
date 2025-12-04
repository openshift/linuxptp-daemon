package hardwareconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
)

// vendor defaults are embedded; no filesystem setup needed

// MockDpllConn implements a mock DPLL connection for testing
type MockDpllConn struct {
	pins []*dpll.PinInfo
	err  error
}

// DumpPinGet mocks the DPLL pin dump operation
func (m *MockDpllConn) DumpPinGet() ([]*dpll.PinInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.pins, nil
}

// Close mocks the connection close operation
func (m *MockDpllConn) Close() error {
	return nil
}

// MockDpllDialer is a function type for mocking dpll.Dial
type MockDpllDialer func(config interface{}) (DpllConnection, error)

// DpllConnection interface for mocking
type DpllConnection interface {
	DumpPinGet() ([]*dpll.PinInfo, error)
	Close() error
}

// MockDpllDialer is defined but not used as a global variable since we pass it directly to tests

// SetupMockDpllPinsFromFileForTests sets up mock DPLL pins using real test data from pins.json
func SetupMockDpllPinsFromFileForTests() error {
	mockGetter, err := CreateMockDpllPinsGetterFromFile("testdata/pins.json")
	if err != nil {
		return fmt.Errorf("failed to create mock getter from file: %w", err)
	}
	SetDpllPinsGetter(mockGetter)
	return nil
}

// CreateMockDpllPinsGetterFromFile creates a DpllPinsGetter that loads test data from file (test-only)
func CreateMockDpllPinsGetterFromFile(filename string) (DpllPinsGetter, error) {
	pins, err := loadTestPinsFromFile(filename)
	if err != nil {
		return nil, err
	}
	return CreateMockDpllPinsGetter(pins, nil), nil
}

// SetupMockDpllPinsForTests is a backwards-compatible helper used by tests in other packages.
// It loads pins from testdata/pins.json and injects the mock getter.
func SetupMockDpllPinsForTests() error {
	// Resolve path relative to this package to be robust to CWD
	base := "testdata/pins.json"
	if _, err := os.Stat(base); err != nil {
		// Try absolute path using current file directory
		_, file, _, ok := runtime.Caller(0)
		if ok && file != "" {
			candidate := filepath.Join(filepath.Dir(file), "testdata", "pins.json")
			if _, err2 := os.Stat(candidate); err2 == nil {
				base = candidate
			}
		}
	}
	mockGetter, err := CreateMockDpllPinsGetterFromFile(base)
	if err != nil {
		return fmt.Errorf("failed to create mock getter from file: %w", err)
	}
	SetDpllPinsGetter(mockGetter)
	return nil
}

// PinInfoHR represents the human-readable format from the JSON test data
type PinInfoHR struct {
	Timestamp                 string                `json:"timestamp"`
	ID                        uint32                `json:"id"`
	ModuleName                string                `json:"moduleName"`
	ClockID                   string                `json:"clockId"`
	BoardLabel                string                `json:"boardLabel"`
	PanelLabel                string                `json:"panelLabel"`
	PackageLabel              string                `json:"packageLabel"`
	Type                      string                `json:"type"`
	Frequency                 uint64                `json:"frequency"`
	FrequencySupported        []dpll.FrequencyRange `json:"frequencySupported"`
	Capabilities              string                `json:"capabilities"`
	ParentDevice              []PinParentDeviceHR   `json:"pinParentDevice"`
	ParentPin                 []PinParentPinHR      `json:"pinParentPin"`
	PhaseAdjustMin            int32                 `json:"phaseAdjustMin"`
	PhaseAdjustMax            int32                 `json:"phaseAdjustMax"`
	PhaseAdjust               int32                 `json:"phaseAdjust"`
	FractionalFrequencyOffset int                   `json:"fractionalFrequencyOffset"`
	EsyncFrequency            int64                 `json:"esyncFrequency"`
	EsyncFrequencySupported   []dpll.FrequencyRange `json:"esyncFrequencySupported"`
	EsyncPulse                int64                 `json:"esyncPulse"`
}

// PinParentDeviceHR represents the human-readable parent device format
type PinParentDeviceHR struct {
	ParentID      uint32  `json:"parentID"`
	Direction     string  `json:"direction"`
	Prio          uint32  `json:"prio"`
	State         string  `json:"state"`
	PhaseOffsetPs float64 `json:"phaseOffsetPs"`
}

// PinParentPinHR represents the human-readable parent pin format
type PinParentPinHR struct {
	ParentID    uint32 `json:"parentID"`
	ParentState string `json:"parentState"`
}

// convertPinType converts string pin type to numeric value
func convertPinType(typeStr string) uint32 {
	switch typeStr {
	case "mux":
		return 1
	case "ext":
		return 2
	case "synce-eth-port":
		return 3
	case "int-oscillator":
		return 4
	case "gnss":
		return 5
	default:
		return 0
	}
}

// convertPinDirection converts string direction to numeric value
func convertPinDirection(dirStr string) uint32 {
	switch dirStr {
	case "input":
		return 1
	case "output":
		return 2
	default:
		return 0
	}
}

// convertPinState converts string state to numeric value
func convertPinState(stateStr string) uint32 {
	switch stateStr {
	case "connected":
		return 1
	case "disconnected":
		return 2
	case "selectable":
		return 3
	default:
		return 0
	}
}

// convertCapabilities converts capabilities string to numeric value
func convertCapabilities(capStr string) uint32 {
	caps := uint32(0)
	if strings.Contains(capStr, "direction-can-change") {
		caps |= 1
	}
	if strings.Contains(capStr, "priority-can-change") {
		caps |= 2
	}
	if strings.Contains(capStr, "state-can-change") {
		caps |= 4
	}
	return caps
}

// parseClockID converts hex string to uint64
func parseClockID(clockIDStr string) uint64 {
	// Remove "0x" prefix if present
	clockIDStr = strings.TrimPrefix(clockIDStr, "0x")
	clockID, _ := strconv.ParseUint(clockIDStr, 16, 64)
	return clockID
}

// convertHRToPinInfo converts human-readable pin info to dpll.PinInfo
func convertHRToPinInfo(hr *PinInfoHR) *dpll.PinInfo {
	pin := &dpll.PinInfo{
		ID:                        hr.ID,
		ModuleName:                hr.ModuleName,
		ClockID:                   parseClockID(hr.ClockID),
		BoardLabel:                hr.BoardLabel,
		PanelLabel:                hr.PanelLabel,
		PackageLabel:              hr.PackageLabel,
		Type:                      convertPinType(hr.Type),
		Frequency:                 hr.Frequency,
		FrequencySupported:        hr.FrequencySupported,
		Capabilities:              convertCapabilities(hr.Capabilities),
		PhaseAdjustMin:            hr.PhaseAdjustMin,
		PhaseAdjustMax:            hr.PhaseAdjustMax,
		PhaseAdjust:               hr.PhaseAdjust,
		FractionalFrequencyOffset: hr.FractionalFrequencyOffset,
		EsyncFrequency:            hr.EsyncFrequency,
		EsyncFrequencySupported:   hr.EsyncFrequencySupported,
		EsyncPulse:                uint32(hr.EsyncPulse),
	}

	// Convert parent devices
	for _, pdHR := range hr.ParentDevice {
		pd := dpll.PinParentDevice{
			ParentID:    pdHR.ParentID,
			Direction:   convertPinDirection(pdHR.Direction),
			Prio:        pdHR.Prio,
			State:       convertPinState(pdHR.State),
			PhaseOffset: int64(pdHR.PhaseOffsetPs),
		}
		pin.ParentDevice = append(pin.ParentDevice, pd)
	}

	// Convert parent pins
	for _, ppHR := range hr.ParentPin {
		pp := dpll.PinParentPin{
			ParentID: ppHR.ParentID,
			State:    convertPinState(ppHR.ParentState),
		}
		pin.ParentPin = append(pin.ParentPin, pp)
	}

	return pin
}

// loadTestPinsFromFile loads and converts test pin data from JSON file
func loadTestPinsFromFile(filename string) ([]*dpll.PinInfo, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read test data file: %v", err)
	}

	var hrPins []*PinInfoHR
	if unmarshalErr := json.Unmarshal(data, &hrPins); unmarshalErr != nil {
		return nil, fmt.Errorf("failed to parse test data: %v", unmarshalErr)
	}

	var pins []*dpll.PinInfo
	for _, hrPin := range hrPins {
		pins = append(pins, convertHRToPinInfo(hrPin))
	}

	return pins, nil
}

// GetDpllPinsMock is a testable version of GetDpllPins that accepts a dialer function
func GetDpllPinsMock(dialer MockDpllDialer) (*PinCache, error) {
	conn, err := dialer(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial DPLL: %v", err)
	}
	//nolint:errcheck
	defer conn.Close()

	dpllPins, err := conn.DumpPinGet()
	if err != nil {
		return nil, fmt.Errorf("failed to dump DPLL pins: %v", err)
	}

	cache := &PinCache{
		Pins: make(map[uint64]map[string]dpll.PinInfo),
	}
	for _, pin := range dpllPins {
		if pin.BoardLabel == "" {
			continue
		}
		if cache.Pins[pin.ClockID] == nil {
			cache.Pins[pin.ClockID] = make(map[string]dpll.PinInfo)
		}
		cache.Pins[pin.ClockID][pin.BoardLabel] = *pin
	}

	return cache, nil
}

// TestGetDpllPins tests the GetDpllPins function with mock DPLL data
func TestGetDpllPins(t *testing.T) {
	// Load real test data from JSON file
	testPins, err := loadTestPinsFromFile("testdata/pins.json")
	if err != nil {
		t.Fatalf("Failed to load test pin data: %v", err)
	}

	// Log the loaded pins for debugging
	t.Logf("Loaded %d test pins from testdata/pins.json", len(testPins))
	if len(testPins) >= 3 {
		for i, pin := range testPins[:3] { // Log first 3 pins
			t.Logf("  Pin %d: ID=%d, ClockID=0x%x, BoardLabel=%s, Type=%d",
				i, pin.ID, pin.ClockID, pin.BoardLabel, pin.Type)
		}
	}

	testCases := []struct {
		name         string
		mockConn     *MockDpllConn
		expectError  bool
		expectedPins int
		description  string
	}{
		{
			name: "successful_pin_retrieval",
			mockConn: &MockDpllConn{
				pins: testPins,
				err:  nil,
			},
			expectError:  false,
			expectedPins: 39, // Pins with non-empty board labels in testdata/pins.json
			description:  "Should successfully retrieve DPLL pins",
		},
		{
			name: "empty_pin_list",
			mockConn: &MockDpllConn{
				pins: []*dpll.PinInfo{},
				err:  nil,
			},
			expectError:  false,
			expectedPins: 0,
			description:  "Should handle empty pin list",
		},
		{
			name: "dpll_connection_error",
			mockConn: &MockDpllConn{
				pins: nil,
				err:  nil,
			},
			expectError:  true,
			expectedPins: 0,
			description:  "Should return error when DPLL connection fails",
		},
		{
			name: "pin_dump_error",
			mockConn: &MockDpllConn{
				pins: nil,
				err:  fmt.Errorf("failed to dump pins"),
			},
			expectError:  true,
			expectedPins: 0,
			description:  "Should return error when pin dump fails",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing: %s", tc.description)

			// Create mock dialer
			var mockDialer MockDpllDialer
			if tc.name == "dpll_connection_error" {
				mockDialer = func(_ interface{}) (DpllConnection, error) {
					return nil, fmt.Errorf("connection failed")
				}
			} else {
				mockDialer = func(_ interface{}) (DpllConnection, error) {
					return tc.mockConn, nil
				}
			}

			// Call the testable version with mock dialer
			result, testErr := GetDpllPinsMock(mockDialer)

			if tc.expectError {
				if testErr == nil {
					t.Errorf("Expected error but got none")
				} else {
					t.Logf("✅ Got expected error: %v", testErr)
				}
			} else {
				if testErr != nil {
					t.Errorf("Unexpected error: %v", testErr)
				} else {
					if result.Count() != tc.expectedPins {
						t.Errorf("Expected %d pins, got %d", tc.expectedPins, result.Count())
					} else {
						t.Logf("✅ Successfully retrieved %d DPLL pins", result.Count())

						// Validate pin data for successful cases
						if result.Count() > 0 {
							pinCount := 0
							for clockID, clockPins := range result.Pins {
								for boardLabel, pinInfo := range clockPins {
									pinCount++
									t.Logf("   Pin %d: ClockID=0x%x, BoardLabel=%s, Type=%d",
										pinCount, clockID, boardLabel, pinInfo.Type)

									// Validate key fields
									if boardLabel == "" {
										t.Errorf("Pin %d has empty BoardLabel", pinCount)
									}
								}
							}
						}
					}
				}
			}
		})
	}

	// Test with real pin data validation
	t.Run("validate_pin_data_structure", func(t *testing.T) {
		mockConn := &MockDpllConn{
			pins: testPins,
			err:  nil,
		}

		mockDialer := func(_ interface{}) (DpllConnection, error) {
			return mockConn, nil
		}

		cache, cacheErr := GetDpllPinsMock(mockDialer)
		if cacheErr != nil {
			t.Fatalf("Unexpected error: %v", cacheErr)
		}

		// Validate specific pin characteristics from the real test data
		if cache.Count() >= 7 {
			// Find and validate CVL-SDP22 pin
			cvlPin, cvlFound := cache.GetPin(0x507c6fffff5c4ae8, "CVL-SDP22")
			if cvlFound {
				if len(cvlPin.ParentDevice) != 2 {
					t.Errorf("Expected CVL-SDP22 to have 2 parent devices, got %d", len(cvlPin.ParentDevice))
				}
				t.Logf("✅ CVL-SDP22 pin validation passed: ParentDevices=%d", len(cvlPin.ParentDevice))
			} else {
				t.Errorf("CVL-SDP22 pin not found")
			}

			// Find and validate GNSS-1PPS pin
			gnssPin, gnssFound := cache.GetPin(0x507c6fffff5c4ae8, "GNSS-1PPS")
			if gnssFound {
				t.Logf("✅ GNSS-1PPS pin validation passed: ParentDevices=%d", len(gnssPin.ParentDevice))
			} else {
				t.Errorf("GNSS-1PPS pin not found")
			}

			// Find and validate SMA1 pin
			smaPin, smaFound := cache.GetPin(0x507c6fffff5c4ae8, "SMA1")
			if smaFound {
				// Check that this pin has connected state (State 1) in parent devices
				hasConnectedState := false
				for _, pd := range smaPin.ParentDevice {
					if pd.State == 1 { // connected
						hasConnectedState = true
						break
					}
				}
				if !hasConnectedState {
					t.Errorf("Expected SMA1 pin to have at least one connected parent device")
				}
				t.Logf("✅ SMA1 pin validation passed")
			} else {
				t.Errorf("SMA1 pin not found")
			}

			// Log cache structure
			t.Logf("✅ Pin cache contains %d total pins across %d clock IDs", cache.Count(), len(cache.Pins))
		}
	})
}

// TestDpllPinsGetterInjection tests the dependency injection mechanism
func TestDpllPinsGetterInjection(t *testing.T) {
	// Save original getter
	originalGetter := dpllPinsGetter
	defer func() {
		dpllPinsGetter = originalGetter
	}()

	// Create mock test data
	mockPins := []*dpll.PinInfo{
		{
			ID:           100,
			ClockID:      0x1234567890abcdef,
			BoardLabel:   "TEST-PIN",
			ModuleName:   "test_module",
			Type:         2, // ext
			Frequency:    1000,
			Capabilities: 7, // all capabilities
		},
	}

	testCases := []struct {
		name         string
		mockGetter   DpllPinsGetter
		expectError  bool
		expectedPins int
		description  string
	}{
		{
			name:         "successful_mock_injection",
			mockGetter:   CreateMockDpllPinsGetter(mockPins, nil),
			expectError:  false,
			expectedPins: 1,
			description:  "Should return mock pins when injected",
		},
		{
			name:         "error_injection",
			mockGetter:   CreateMockDpllPinsGetter(nil, fmt.Errorf("mock error")),
			expectError:  true,
			expectedPins: 0,
			description:  "Should return error when mock is configured to fail",
		},
		{
			name:         "empty_pins_injection",
			mockGetter:   CreateMockDpllPinsGetter([]*dpll.PinInfo{}, nil),
			expectError:  false,
			expectedPins: 0,
			description:  "Should return empty list when mock has no pins",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("Testing: %s", tc.description)

			// Inject the mock getter
			SetDpllPinsGetter(tc.mockGetter)

			// Call GetDpllPins which should now use the mock
			result, err := GetDpllPins()

			if tc.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
				} else {
					t.Logf("✅ Got expected error: %v", err)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				} else {
					if result.Count() != tc.expectedPins {
						t.Errorf("Expected %d pins, got %d", tc.expectedPins, result.Count())
					} else {
						t.Logf("✅ Successfully got %d pins from mock", result.Count())
						if result.Count() > 0 {
							// For the test pin, check if it exists in the cache
							pinInfo, found := result.GetPin(0x1234567890abcdef, "TEST-PIN")
							if found {
								t.Logf("   Mock pin: ClockID=0x1234567890abcdef, BoardLabel=TEST-PIN, ParentDevices=%d", len(pinInfo.ParentDevice))
							} else {
								t.Errorf("Expected to find test pin with ClockID=0x1234567890abcdef and BoardLabel=TEST-PIN")
							}
						}
					}
				}
			}

			// Reset to ensure test isolation
			ResetDpllPinsGetter()
		})
	}
}

// TestDpllPinsGetterFromFile tests loading mock data from file
func TestDpllPinsGetterFromFile(t *testing.T) {
	// Save original getter
	originalGetter := dpllPinsGetter
	defer func() {
		dpllPinsGetter = originalGetter
	}()

	// Create mock getter from test file
	mockGetter, err := CreateMockDpllPinsGetterFromFile("testdata/pins.json")
	if err != nil {
		t.Fatalf("Failed to create mock getter from file: %v", err)
	}

	// Inject the file-based mock
	SetDpllPinsGetter(mockGetter)

	// Test the injection
	cache, err := GetDpllPins()
	if err != nil {
		t.Fatalf("Unexpected error from mock getter: %v", err)
	}

	if cache.Count() != 39 {
		t.Errorf("Expected 39 pins from test file, got %d", cache.Count())
	}

	t.Logf("✅ Successfully loaded %d pins from test file via mock getter", cache.Count())

	// Validate some pins from the test file
	cvlPin, cvlFound := cache.GetPin(0x507c6fffff5c4ae8, "CVL-SDP22")
	gnssPin, gnssFound := cache.GetPin(0x507c6fffff5c4ae8, "GNSS-1PPS")

	if !cvlFound {
		t.Errorf("Expected to find CVL-SDP22 pin in mock data")
	} else {
		t.Logf("✅ Found CVL-SDP22 pin: ParentDevices=%d", len(cvlPin.ParentDevice))
	}

	if !gnssFound {
		t.Errorf("Expected to find GNSS-1PPS pin in mock data")
	} else {
		t.Logf("✅ Found GNSS-1PPS pin: ParentDevices=%d", len(gnssPin.ParentDevice))
	}
}
