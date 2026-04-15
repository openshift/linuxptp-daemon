package intel

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	"github.com/stretchr/testify/assert"
)

// mockSysfsPins sets up MockFileSystem so each pin exists as a readable
// sysfs file under the device's PHC. If SMA1 is in the list,
// hasSysfsSMAPins(device) will also return true.
func mockSysfsPins(mockFS *MockFileSystem, device string, pins []string) {
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
	mockFS.AllowReadDir(fmt.Sprintf("/sys/class/net/%s/device/ptp/", device), phcEntries, nil)
	for _, pin := range pins {
		mockFS.AllowReadFile(fmt.Sprintf("/sys/class/net/%s/device/ptp/ptp0/pins/%s", device, pin), []byte("0 1"), nil)
	}
}

// mockNoSysfsSMA sets up MockFileSystem so hasSysfsSMAPins(device) returns false.
func mockNoSysfsSMA(mockFS *MockFileSystem, device string) { //nolint:unparam // device varies by test scenario
	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
	mockFS.AllowReadDir(fmt.Sprintf("/sys/class/net/%s/device/ptp/", device), phcEntries, nil)
	mockFS.AllowReadFile(fmt.Sprintf("/sys/class/net/%s/device/ptp/ptp0/pins/SMA1", device), nil, os.ErrNotExist)
}

func TestValidateE810Opts_UnknownFields(t *testing.T) {
	tests := []struct {
		name      string
		config    map[string]interface{}
		expectErr bool
	}{
		{
			name: "valid config with known fields",
			config: map[string]interface{}{
				"enableDefaultConfig": true,
				"devices":             []string{"ens4f0"},
			},
			expectErr: false,
		},
		{
			name: "unknown top-level field (typo)",
			config: map[string]interface{}{
				"enableDefautConfig": true, // typo: missing 'l'
			},
			expectErr: true,
		},
		{
			name: "completely unknown field",
			config: map[string]interface{}{
				"foobar": "baz",
			},
			expectErr: true,
		},
		{
			name:      "empty config is valid",
			config:    map[string]interface{}{},
			expectErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.config)
			errs := ValidateE810Opts(raw)
			if tc.expectErr {
				assert.NotEmpty(t, errs, "expected validation errors")
			} else {
				assert.Empty(t, errs, "expected no validation errors but got: %v", errs)
			}
		})
	}
}

func TestValidateE810Opts_InvalidPinNames_RuntimeDiscovery(t *testing.T) {
	mockFS, cleanup := setupMockFS()
	defer cleanup()
	mockSysfsPins(mockFS, "ens4f0", []string{"SMA1", "SMA2", "U.FL1", "U.FL2"})

	tests := []struct {
		name      string
		config    map[string]interface{}
		expectErr bool
		errSubstr string
	}{
		{
			name: "valid pin names from sysfs",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"ens4f0": map[string]string{
						"SMA1":  "2 1",
						"SMA2":  "2 2",
						"U.FL1": "0 1",
						"U.FL2": "0 2",
					},
				},
			},
			expectErr: false,
		},
		{
			name: "typo in pin name detected via sysfs",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"ens4f0": map[string]string{
						"xxxSMA1": "2 1",
					},
				},
			},
			expectErr: true,
			errSubstr: "xxxSMA1",
		},
		{
			name: "wrong case pin name detected via sysfs",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"ens4f0": map[string]string{
						"sma1": "2 1",
					},
				},
			},
			expectErr: true,
			errSubstr: "sma1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.config)
			errs := ValidateE810Opts(raw)
			if tc.expectErr {
				assert.NotEmpty(t, errs, "expected validation errors")
				found := false
				for _, e := range errs {
					if contains(e, tc.errSubstr) {
						found = true
					}
				}
				assert.True(t, found, "expected error containing '%s', got: %v", tc.errSubstr, errs)
			} else {
				assert.Empty(t, errs, "expected no validation errors but got: %v", errs)
			}
		})
	}
}

func TestValidateE810Opts_ErrorWhenBothSysfsAndDPLLFail(t *testing.T) {
	mockFS, cleanupFS := setupMockFS()
	defer cleanupFS()
	mockNoSysfsSMA(mockFS, "ens4f0")

	_, cleanupDPLL := setupMockDPLLPins()
	defer cleanupDPLL()

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"ens4f0": map[string]string{"SMA1": "2 1"},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE810Opts(raw)
	assert.NotEmpty(t, errs)
	found := false
	for _, e := range errs {
		if contains(e, "SMA1") && contains(e, "not found in DPLL") {
			found = true
		}
	}
	assert.True(t, found, "expected error about SMA1 not in DPLL labels, got: %v", errs)
}

func TestValidateE810Opts_DynamicPinDiscovery(t *testing.T) {
	mockFS, cleanup := setupMockFS()
	defer cleanup()
	mockSysfsPins(mockFS, "ens4f0", []string{"CUSTOM_PIN", "SMA1"})

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"ens4f0": map[string]string{
				"CUSTOM_PIN": "2 1",
			},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE810Opts(raw)
	assert.Empty(t, errs, "CUSTOM_PIN should be valid when sysfs reports it, got: %v", errs)

	config2 := map[string]interface{}{
		"pins": map[string]interface{}{
			"ens4f0": map[string]string{
				"U.FL2": "0 2",
			},
		},
	}
	raw2, _ := json.Marshal(config2)
	errs2 := ValidateE810Opts(raw2)
	assert.NotEmpty(t, errs2, "U.FL2 should be invalid when sysfs doesn't report it")
}

func TestValidatePinNames_E810_DPLLPath_NewerKernel(t *testing.T) {
	mockFS, cleanupFS := setupMockFS()
	defer cleanupFS()
	mockNoSysfsSMA(mockFS, "ens4f0")

	_, cleanupDPLL := setupMockDPLLPins(
		&dpll.PinInfo{BoardLabel: "SMA1"},
		&dpll.PinInfo{BoardLabel: "SMA2"},
		&dpll.PinInfo{BoardLabel: "U.FL1"},
	)
	defer cleanupDPLL()

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"ens4f0": map[string]string{
				"SMA1":  "2 1",
				"U.FL1": "0 1",
			},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE810Opts(raw)
	assert.Empty(t, errs, "all pins should be valid via DPLL, got: %v", errs)
}

func TestValidatePinNames_E810_DPLLPath_SysfsUnavailable(t *testing.T) {
	mockFS, cleanupFS := setupMockFS()
	defer cleanupFS()
	mockNoSysfsSMA(mockFS, "ens4f0")

	_, cleanupDPLL := setupMockDPLLPins(
		&dpll.PinInfo{BoardLabel: "SMA1"},
		&dpll.PinInfo{BoardLabel: "SMA2"},
	)
	defer cleanupDPLL()

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"ens4f0": map[string]string{"SMA1": "2 1"},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE810Opts(raw)
	assert.Empty(t, errs, "SMA1 should be valid via DPLL, got: %v", errs)
}

func TestValidatePinNames_E810_DPLLPath_BogusPin(t *testing.T) {
	mockFS, cleanupFS := setupMockFS()
	defer cleanupFS()
	mockNoSysfsSMA(mockFS, "ens4f0")

	_, cleanupDPLL := setupMockDPLLPins(
		&dpll.PinInfo{BoardLabel: "SMA1"},
		&dpll.PinInfo{BoardLabel: "SMA2"},
	)
	defer cleanupDPLL()

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"ens4f0": map[string]string{"BOGUS_PIN": "2 1"},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE810Opts(raw)
	assert.NotEmpty(t, errs, "BOGUS_PIN should not be valid in DPLL labels")
	found := false
	for _, e := range errs {
		if contains(e, "BOGUS_PIN") {
			found = true
		}
	}
	assert.True(t, found, "expected error about BOGUS_PIN, got: %v", errs)
}

func TestValidatePinValues(t *testing.T) {
	mockFS, cleanup := setupMockFS()
	defer cleanup()
	mockSysfsPins(mockFS, "ens4f0", []string{"SMA1", "SMA2", "U.FL1", "U.FL2"})

	tests := []struct {
		name      string
		config    map[string]interface{}
		expectErr bool
		errSubstr string
	}{
		{
			name: "valid pin values",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"ens4f0": map[string]string{
						"SMA1": "0 1",
						"SMA2": "2 2",
					},
				},
			},
			expectErr: false,
		},
		{
			name: "garbage pin value",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"ens4f0": map[string]string{
						"SMA1": "abc xyz",
					},
				},
			},
			expectErr: true,
			errSubstr: "invalid direction",
		},
		{
			name: "too many parts in value",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"ens4f0": map[string]string{
						"SMA1": "0 1 2",
					},
				},
			},
			expectErr: true,
			errSubstr: "invalid pin value",
		},
		{
			name: "single value instead of pair",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"ens4f0": map[string]string{
						"SMA1": "1",
					},
				},
			},
			expectErr: true,
			errSubstr: "invalid pin value",
		},
		{
			name: "direction out of range (3)",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"ens4f0": map[string]string{
						"SMA1": "3 1",
					},
				},
			},
			expectErr: true,
			errSubstr: "invalid direction",
		},
		{
			name: "negative channel",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"ens4f0": map[string]string{
						"SMA1": "0 -1",
					},
				},
			},
			expectErr: true,
			errSubstr: "invalid channel",
		},
		{
			name: "empty pin value",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"ens4f0": map[string]string{
						"SMA1": "",
					},
				},
			},
			expectErr: true,
			errSubstr: "invalid pin value",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.config)
			errs := ValidateE810Opts(raw)
			if tc.expectErr {
				assert.NotEmpty(t, errs, "expected validation errors")
				found := false
				for _, e := range errs {
					if contains(e, tc.errSubstr) {
						found = true
					}
				}
				assert.True(t, found, "expected error containing '%s', got: %v", tc.errSubstr, errs)
			} else {
				assert.Empty(t, errs, "expected no validation errors but got: %v", errs)
			}
		})
	}
}

func TestValidateInterconnections_Direct(t *testing.T) {
	tests := []struct {
		name      string
		inputs    []PhaseInputs
		expectErr bool
		errSubstr string
	}{
		{
			name:      "empty list is valid",
			inputs:    []PhaseInputs{},
			expectErr: false,
		},
		{
			name: "valid entry with gnssInput",
			inputs: []PhaseInputs{
				{
					ID:                    "ens4f0",
					Part:                  "E810-XXVDA4T",
					GnssInput:             true,
					PhaseOutputConnectors: []string{"SMA1", "SMA2"},
				},
			},
			expectErr: false,
		},
		{
			name: "valid entry with upstreamPort",
			inputs: []PhaseInputs{
				{
					ID:           "ens5f0",
					Part:         "E810-XXVDA4T",
					UpstreamPort: "ens4f0",
				},
			},
			expectErr: false,
		},
		{
			name: "valid entry with inputConnector",
			inputs: []PhaseInputs{
				{
					ID:   "ens4f0",
					Part: "E810-XXVDA4T",
					Input: InputConnector{
						Connector: "SMA1",
						DelayPs:   920,
					},
				},
			},
			expectErr: false,
		},
		{
			name: "missing id",
			inputs: []PhaseInputs{
				{
					Part:      "E810-XXVDA4T",
					GnssInput: true,
				},
			},
			expectErr: true,
			errSubstr: "'id' field is required",
		},
		{
			name: "missing Part",
			inputs: []PhaseInputs{
				{
					ID:        "ens4f0",
					GnssInput: true,
				},
			},
			expectErr: true,
			errSubstr: "'Part' field is required",
		},
		{
			name: "unknown Part (typo)",
			inputs: []PhaseInputs{
				{
					ID:        "ens4f0",
					Part:      "E810-XXVDA4",
					GnssInput: true,
				},
			},
			expectErr: true,
			errSubstr: "unknown Part",
		},
		{
			name: "no input source specified",
			inputs: []PhaseInputs{
				{
					ID:   "ens4f0",
					Part: "E810-XXVDA4T",
				},
			},
			expectErr: true,
			errSubstr: "must specify either",
		},
		{
			name: "invalid inputConnector name",
			inputs: []PhaseInputs{
				{
					ID:   "ens4f0",
					Part: "E810-XXVDA4T",
					Input: InputConnector{
						Connector: "BOGUS_CONN",
					},
				},
			},
			expectErr: true,
			errSubstr: "BOGUS_CONN",
		},
		{
			name: "invalid phaseOutputConnector name",
			inputs: []PhaseInputs{
				{
					ID:                    "ens4f0",
					Part:                  "E810-XXVDA4T",
					GnssInput:             true,
					PhaseOutputConnectors: []string{"SMA1", "BADCONN"},
				},
			},
			expectErr: true,
			errSubstr: "BADCONN",
		},
		{
			name: "multiple entries with errors on different indices",
			inputs: []PhaseInputs{
				{
					ID:        "ens4f0",
					Part:      "E810-XXVDA4T",
					GnssInput: true,
				},
				{
					Part:      "E810-XXVDA4T",
					GnssInput: true,
				},
			},
			expectErr: true,
			errSubstr: "interconnections[1]",
		},
		{
			name: "missing both id and Part",
			inputs: []PhaseInputs{
				{},
			},
			expectErr: true,
			errSubstr: "'id' field is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateInterconnections(tc.inputs)
			if tc.expectErr {
				assert.NotEmpty(t, errs, "expected validation errors")
				found := false
				for _, e := range errs {
					if contains(e, tc.errSubstr) {
						found = true
					}
				}
				assert.True(t, found, "expected error containing '%s', got: %v", tc.errSubstr, errs)
			} else {
				assert.Empty(t, errs, "expected no validation errors but got: %v", errs)
			}
		})
	}
}

func TestValidateE810Opts_InvalidInterconnections(t *testing.T) {
	tests := []struct {
		name      string
		config    map[string]interface{}
		expectErr bool
		errSubstr string
	}{
		{
			name: "valid interconnections",
			config: map[string]interface{}{
				"interconnections": []map[string]interface{}{
					{
						"id":                    "ens4f0",
						"Part":                  "E810-XXVDA4T",
						"gnssInput":             true,
						"phaseOutputConnectors": []string{"SMA1", "SMA2"},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "missing id field",
			config: map[string]interface{}{
				"interconnections": []map[string]interface{}{
					{
						"Part":      "E810-XXVDA4T",
						"gnssInput": true,
					},
				},
			},
			expectErr: true,
			errSubstr: "'id' field is required",
		},
		{
			name: "invalid Part name (typo)",
			config: map[string]interface{}{
				"interconnections": []map[string]interface{}{
					{
						"id":        "ens4f0",
						"Part":      "E810-XXVDA4",
						"gnssInput": true,
					},
				},
			},
			expectErr: true,
			errSubstr: "unknown Part",
		},
		{
			name: "invalid input connector",
			config: map[string]interface{}{
				"interconnections": []map[string]interface{}{
					{
						"id":   "ens4f0",
						"Part": "E810-XXVDA4T",
						"inputConnector": map[string]interface{}{
							"connector": "xxxSMA1",
							"delayPs":   920,
						},
					},
				},
			},
			expectErr: true,
			errSubstr: "xxxSMA1",
		},
		{
			name: "invalid phaseOutputConnector",
			config: map[string]interface{}{
				"interconnections": []map[string]interface{}{
					{
						"id":                    "ens4f0",
						"Part":                  "E810-XXVDA4T",
						"gnssInput":             true,
						"phaseOutputConnectors": []string{"BADCONN"},
					},
				},
			},
			expectErr: true,
			errSubstr: "BADCONN",
		},
		{
			name: "missing Part field",
			config: map[string]interface{}{
				"interconnections": []map[string]interface{}{
					{
						"id":        "ens4f0",
						"gnssInput": true,
					},
				},
			},
			expectErr: true,
			errSubstr: "'Part' field is required",
		},
		{
			name: "missing input source (no gnss, no upstream, no connector)",
			config: map[string]interface{}{
				"interconnections": []map[string]interface{}{
					{
						"id":   "ens5f0",
						"Part": "E810-XXVDA4T",
					},
				},
			},
			expectErr: true,
			errSubstr: "must specify either",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.config)
			errs := ValidateE810Opts(raw)
			if tc.expectErr {
				assert.NotEmpty(t, errs, "expected validation errors")
				found := false
				for _, e := range errs {
					if contains(e, tc.errSubstr) {
						found = true
					}
				}
				assert.True(t, found, "expected error containing '%s', got: %v", tc.errSubstr, errs)
			} else {
				assert.Empty(t, errs, "expected no validation errors but got: %v", errs)
			}
		})
	}
}

func TestValidateE825Opts_UnknownFields(t *testing.T) {
	tests := []struct {
		name      string
		config    map[string]interface{}
		expectErr bool
	}{
		{
			name: "valid E825 config",
			config: map[string]interface{}{
				"devices": []string{"eno5"},
				"gnss":    map[string]interface{}{"disabled": true},
			},
			expectErr: false,
		},
		{
			name: "enableDefaultConfig is valid for E825",
			config: map[string]interface{}{
				"enableDefaultConfig": false,
				"devices":             []string{"eno5"},
			},
			expectErr: false,
		},
		{
			name: "typo in gnss field",
			config: map[string]interface{}{
				"gns": map[string]interface{}{"disabled": true},
			},
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.config)
			errs := ValidateE825Opts(raw)
			if tc.expectErr {
				assert.NotEmpty(t, errs, "expected validation errors")
			} else {
				assert.Empty(t, errs, "expected no validation errors but got: %v", errs)
			}
		})
	}
}

func TestValidateE830Opts_UnknownFields(t *testing.T) {
	tests := []struct {
		name      string
		config    map[string]interface{}
		expectErr bool
	}{
		{
			name: "valid E830 config",
			config: map[string]interface{}{
				"devices": []string{"enp108s0f0"},
			},
			expectErr: false,
		},
		{
			name: "unknown field in E830",
			config: map[string]interface{}{
				"interconnections": []map[string]interface{}{},
			},
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.config)
			errs := ValidateE830Opts(raw)
			if tc.expectErr {
				assert.NotEmpty(t, errs, "expected validation errors")
			} else {
				assert.Empty(t, errs, "expected no validation errors but got: %v", errs)
			}
		})
	}
}

func TestDiscoverPHCPins(t *testing.T) {
	mockFS, cleanup := setupMockFS()
	defer cleanup()

	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
	pinEntries := []os.DirEntry{
		MockDirEntry{name: "SDP0"},
		MockDirEntry{name: "SDP1"},
		MockDirEntry{name: "SDP2"},
		MockDirEntry{name: "SDP3"},
	}
	mockFS.AllowReadDir("/sys/class/net/eno5/device/ptp/", phcEntries, nil)
	mockFS.AllowReadDir("/sys/class/net/eno5/device/ptp/ptp0/pins/", pinEntries, nil)

	pins, err := discoverPHCPins("eno5")
	assert.NoError(t, err)
	assert.ElementsMatch(t, []string{"SDP0", "SDP1", "SDP2", "SDP3"}, pins)
}

func TestDiscoverPHCPins_MultiplePHCs(t *testing.T) {
	mockFS, cleanup := setupMockFS()
	defer cleanup()

	phcEntries := []os.DirEntry{
		MockDirEntry{name: "ptp0", isDir: true},
		MockDirEntry{name: "ptp1", isDir: true},
	}
	pinEntries0 := []os.DirEntry{
		MockDirEntry{name: "SMA1"},
		MockDirEntry{name: "SMA2"},
	}
	pinEntries1 := []os.DirEntry{
		MockDirEntry{name: "SMA1"},
		MockDirEntry{name: "U.FL1"},
	}
	mockFS.AllowReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
	mockFS.AllowReadDir("/sys/class/net/ens4f0/device/ptp/ptp0/pins/", pinEntries0, nil)
	mockFS.AllowReadDir("/sys/class/net/ens4f0/device/ptp/ptp1/pins/", pinEntries1, nil)

	pins, err := discoverPHCPins("ens4f0")
	assert.NoError(t, err)
	assert.ElementsMatch(t, []string{"SMA1", "SMA2", "U.FL1"}, pins)
}

func TestValidatePinNames_E810_DPLLPathWhenNoSysfsSMA(t *testing.T) {
	mockFS, cleanupFS := setupMockFS()
	defer cleanupFS()
	mockNoSysfsSMA(mockFS, "ens4f0")

	_, cleanupDPLL := setupMockDPLLPins(
		&dpll.PinInfo{BoardLabel: "SMA1"},
		&dpll.PinInfo{BoardLabel: "U.FL1"},
	)
	defer cleanupDPLL()

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"ens4f0": map[string]string{
				"SMA1":  "2 1",
				"U.FL1": "0 1",
			},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE810Opts(raw)
	assert.Empty(t, errs, "all pins should be valid via DPLL when hasSysfsSMAPins is false, got: %v", errs)
}

func TestValidatePinNames_E810_SysfsPathWhenSMAExists(t *testing.T) {
	mockFS, cleanup := setupMockFS()
	defer cleanup()
	mockSysfsPins(mockFS, "ens4f0", []string{"SMA1", "SMA2", "U.FL1", "U.FL2"})

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"ens4f0": map[string]string{"SMA1": "2 1"},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE810Opts(raw)
	assert.Empty(t, errs, "SMA1 should be valid via sysfs, got: %v", errs)

	config2 := map[string]interface{}{
		"pins": map[string]interface{}{
			"ens4f0": map[string]string{"BOGUS": "2 1"},
		},
	}
	raw2, _ := json.Marshal(config2)
	errs2 := ValidateE810Opts(raw2)
	assert.NotEmpty(t, errs2, "BOGUS should be invalid when not in sysfs")
}

func TestValidatePinNames_E810_DPLLPath_InvalidPin(t *testing.T) {
	mockFS, cleanupFS := setupMockFS()
	defer cleanupFS()
	mockNoSysfsSMA(mockFS, "ens4f0")

	_, cleanupDPLL := setupMockDPLLPins(
		&dpll.PinInfo{BoardLabel: "SMA1"},
	)
	defer cleanupDPLL()

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"ens4f0": map[string]string{"BOGUS_PIN": "2 1"},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE810Opts(raw)
	assert.NotEmpty(t, errs, "BOGUS_PIN should be invalid when not in DPLL labels")
	found := false
	for _, e := range errs {
		if contains(e, "BOGUS_PIN") {
			found = true
		}
	}
	assert.True(t, found, "expected error about BOGUS_PIN, got: %v", errs)
}

func TestValidatePinNames_E825_AlwaysUsesSysfs(t *testing.T) {
	mockFS, cleanup := setupMockFS()
	defer cleanup()
	mockSysfsPins(mockFS, "eno5", []string{"SDP0", "SDP1", "SDP2", "SDP3"})

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"eno5": map[string]string{"SDP0": "2 1"},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE825Opts(raw)
	assert.Empty(t, errs, "SDP0 should be valid via sysfs for E825, got: %v", errs)

	config2 := map[string]interface{}{
		"pins": map[string]interface{}{
			"eno5": map[string]string{
				"SDP0": "2 1",
				"BADP": "0 0",
			},
		},
	}
	raw2, _ := json.Marshal(config2)
	errs2 := ValidateE825Opts(raw2)
	assert.NotEmpty(t, errs2, "BADP should be invalid for E825 (sysfs-only)")
	found := false
	for _, e := range errs2 {
		if contains(e, "BADP") {
			found = true
		}
	}
	assert.True(t, found, "expected error about BADP, got: %v", errs2)
}

func TestValidatePinNames_E830_AlwaysUsesSysfs(t *testing.T) {
	mockFS, cleanup := setupMockFS()
	defer cleanup()
	mockSysfsPins(mockFS, "enp108s0f0", []string{"PGT0", "PGT1", "PGT2", "PGT3"})

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"enp108s0f0": map[string]string{"PGT0": "2 1"},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE830Opts(raw)
	assert.Empty(t, errs, "PGT0 should be valid via sysfs for E830, got: %v", errs)

	config2 := map[string]interface{}{
		"pins": map[string]interface{}{
			"enp108s0f0": map[string]string{"BOGUS": "2 1"},
		},
	}
	raw2, _ := json.Marshal(config2)
	errs2 := ValidateE830Opts(raw2)
	assert.NotEmpty(t, errs2, "BOGUS should be invalid for E830 (sysfs-only)")
}

func TestValidateE830Opts_GNRDFullValidation(t *testing.T) {
	mockFS, cleanup := setupMockFS()
	defer cleanup()
	mockSysfsPins(mockFS, "enp108s0f0", []string{"PGT0", "PGT1", "PGT2", "PGT3"})

	tests := []struct {
		name      string
		config    map[string]interface{}
		expectErr bool
		errSubstr string
	}{
		{
			name: "valid GNR-D config with all PGT pins",
			config: map[string]interface{}{
				"devices": []string{"enp108s0f0"},
				"pins": map[string]interface{}{
					"enp108s0f0": map[string]string{
						"PGT0": "2 1",
						"PGT1": "0 1",
						"PGT2": "0 2",
						"PGT3": "0 3",
					},
				},
			},
			expectErr: false,
		},
		{
			name: "valid GNR-D config with frequencies",
			config: map[string]interface{}{
				"devices":     []string{"enp108s0f0"},
				"frequencies": map[string]interface{}{},
			},
			expectErr: false,
		},
		{
			name: "valid GNR-D config with settings",
			config: map[string]interface{}{
				"devices":  []string{"enp108s0f0"},
				"settings": map[string]interface{}{},
			},
			expectErr: false,
		},
		{
			name: "valid GNR-D config with phaseOffsetPins",
			config: map[string]interface{}{
				"devices":         []string{"enp108s0f0"},
				"phaseOffsetPins": map[string]interface{}{},
			},
			expectErr: false,
		},
		{
			name: "GNR-D typo in pin name (SMA1 instead of PGT)",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"enp108s0f0": map[string]string{
						"SMA1": "2 1",
					},
				},
			},
			expectErr: true,
			errSubstr: "SMA1",
		},
		{
			name: "GNR-D invalid pin value format",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"enp108s0f0": map[string]string{
						"PGT0": "abc",
					},
				},
			},
			expectErr: true,
			errSubstr: "invalid pin value",
		},
		{
			name: "GNR-D direction out of range",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"enp108s0f0": map[string]string{
						"PGT0": "5 1",
					},
				},
			},
			expectErr: true,
			errSubstr: "invalid direction",
		},
		{
			name: "GNR-D negative channel",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"enp108s0f0": map[string]string{
						"PGT0": "2 -1",
					},
				},
			},
			expectErr: true,
			errSubstr: "invalid channel",
		},
		{
			name: "GNR-D unknown top-level field (typo)",
			config: map[string]interface{}{
				"devics": []string{"enp108s0f0"},
			},
			expectErr: true,
			errSubstr: "unknown",
		},
		{
			name:      "GNR-D empty config is valid",
			config:    map[string]interface{}{},
			expectErr: false,
		},
		{
			name: "GNR-D interconnections field rejected (E810-only)",
			config: map[string]interface{}{
				"interconnections": []map[string]interface{}{},
			},
			expectErr: true,
			errSubstr: "unknown",
		},
		{
			name: "GNR-D multiple pin errors accumulate",
			config: map[string]interface{}{
				"pins": map[string]interface{}{
					"enp108s0f0": map[string]string{
						"BOGUS1": "2 1",
						"BOGUS2": "0 1",
					},
				},
			},
			expectErr: true,
			errSubstr: "BOGUS",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.config)
			errs := ValidateE830Opts(raw)
			if tc.expectErr {
				assert.NotEmpty(t, errs, "expected validation errors")
				found := false
				for _, e := range errs {
					if contains(e, tc.errSubstr) {
						found = true
					}
				}
				assert.True(t, found, "expected error containing '%s', got: %v", tc.errSubstr, errs)
			} else {
				assert.Empty(t, errs, "expected no validation errors but got: %v", errs)
			}
		})
	}
}

func TestValidatePinNames_SysfsDiscoveryError(t *testing.T) {
	mockFS, cleanup := setupMockFS()
	defer cleanup()

	mockFS.AllowReadDir("/sys/class/net/eno5/device/ptp/", nil, fmt.Errorf("no such directory"))

	config := map[string]interface{}{
		"pins": map[string]interface{}{
			"eno5": map[string]string{"SDP0": "2 1"},
		},
	}
	raw, _ := json.Marshal(config)
	errs := ValidateE825Opts(raw)
	assert.NotEmpty(t, errs, "should error when sysfs discovery fails")
	found := false
	for _, e := range errs {
		if contains(e, "cannot discover sysfs pins") {
			found = true
		}
	}
	assert.True(t, found, "expected sysfs discovery error, got: %v", errs)
}

func contains(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) && stringContains(s, substr)
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
