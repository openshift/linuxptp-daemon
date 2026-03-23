package intel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

// discoverSysfsPinsFunc is the function used to discover pins from sysfs.
// It is a variable so it can be replaced in tests.
var discoverSysfsPinsFunc = discoverPHCPins

// hasDPLLPinLabelFunc checks whether a pin name exists as a DPLL board label.
// It is a variable so it can be replaced in tests.
var hasDPLLPinLabelFunc = hasDPLLPinLabel

// hasSysfsSMAPinsFunc checks whether SMA pins are available via sysfs for a device.
// It is a variable so it can be replaced in tests.
var hasSysfsSMAPinsFunc = hasSysfsSMAPins

func hasDPLLPinLabel(pinName string) bool {
	return len(DpllPins.GetAllPinsByLabel(pinName)) > 0
}

// discoverPHCPins reads available PHC pin names from sysfs for a given device.
// Returns the pin names found under /sys/class/net/<device>/device/ptp/<phc>/pins/
func discoverPHCPins(device string) ([]string, error) {
	deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", device)
	phcs, deviceDirErr := filesystem.ReadDir(deviceDir)
	if deviceDirErr != nil {
		return nil, fmt.Errorf("cannot read PTP device directory %s: %w", deviceDir, deviceDirErr)
	}
	seen := map[string]bool{}
	var result []string
	for _, phc := range phcs {
		pinsDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/pins/", device, phc.Name())
		pins, pinsDirErr := filesystem.ReadDir(pinsDir)
		if pinsDirErr != nil {
			return nil, fmt.Errorf("cannot read PTP pins directory %s: %w", pinsDir, pinsDirErr)
		}
		for _, pin := range pins {
			if !seen[pin.Name()] {
				seen[pin.Name()] = true
				result = append(result, pin.Name())
			}
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no PHC pins found for device %s", device)
	}
	return result, nil
}

// validConnectorNames are valid connector names from the hardware spec
func validConnectorNames() []string {
	connectors := map[string]bool{}
	for _, specYaml := range hardware {
		delays := InternalDelays{}
		b := []byte(specYaml)
		if err := yaml.Unmarshal(b, &delays); err != nil {
			continue
		}
		for _, link := range delays.ExternalInputs {
			connectors[strings.ToLower(link.Connector)] = true
		}
		for _, link := range delays.ExternalOutputs {
			connectors[strings.ToLower(link.Connector)] = true
		}
		if delays.GnssInput.Connector != "" {
			connectors[strings.ToLower(delays.GnssInput.Connector)] = true
		}
	}
	result := make([]string, 0, len(connectors))
	for c := range connectors {
		result = append(result, c)
	}
	return result
}

// validPartNames returns valid part names from the hardware map
func validPartNames() []string {
	parts := make([]string, 0, len(hardware))
	for k := range hardware {
		parts = append(parts, k)
	}
	return parts
}

// validateUnknownFields checks for unknown JSON fields by re-decoding with DisallowUnknownFields.
// It returns a list of human-readable error messages for any unknown fields found.
func validateUnknownFields(rawJSON []byte, target interface{}) []string {
	decoder := json.NewDecoder(bytes.NewReader(rawJSON))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(target)
	if err != nil {
		// Extract the unknown field name from the error message
		errStr := err.Error()
		if strings.Contains(errStr, "unknown field") {
			return []string{fmt.Sprintf("unknown configuration field: %s", errStr)}
		}
		return []string{fmt.Sprintf("configuration parse error: %s", errStr)}
	}
	return nil
}

// validatePinNames checks that all pin names in the DevicePins map are valid.
// The useSysfs strategy function decides per device whether to validate against
// sysfs pins or DPLL board labels. This mirrors each plugin's runtime behavior:
// - E810: passes hasSysfsSMAPins (sysfs when SMA1 exists, DPLL otherwise)
// - E825/E830: passes alwaysSysfs (always sysfs, matching pinConfig.applyPinSet)
func validatePinNames(devicePins map[string]pinSet, pluginName string, useSysfs func(string) bool) []string {
	var errs []string
	for device, pins := range devicePins {
		if useSysfs(device) {
			// sysfs path: validate all pins against discovered sysfs pins
			sysfsPins, sysfsErr := discoverSysfsPinsFunc(device)
			if sysfsErr != nil {
				errs = append(errs, fmt.Sprintf(
					"%s plugin: cannot discover sysfs pins for device '%s': %v",
					pluginName, device, sysfsErr))
				continue
			}
			validSet := make(map[string]bool, len(sysfsPins))
			for _, p := range sysfsPins {
				validSet[p] = true
			}
			for pinName := range pins {
				if !validSet[pinName] {
					errs = append(errs, fmt.Sprintf(
						"%s plugin: unknown pin name '%s' for device '%s' (valid sysfs pins: %s)",
						pluginName, pinName, device, strings.Join(sysfsPins, ", ")))
				}
			}
		} else {
			// DPLL path: validate all pins against DPLL board labels
			for pinName := range pins {
				if !hasDPLLPinLabelFunc(pinName) {
					errs = append(errs, fmt.Sprintf(
						"%s plugin: unknown pin name '%s' for device '%s' (not found in DPLL pin labels)",
						pluginName, pinName, device))
				}
			}
		}
	}
	return errs
}

// validatePinValues checks that pin values have the expected "<direction> <channel>" format
// where direction is 0-2 and channel is a non-negative integer
func validatePinValues(devicePins map[string]pinSet, pluginName string) []string {
	var errs []string
	for device, pins := range devicePins {
		for pinName, value := range pins {
			parts := strings.Fields(value)
			if len(parts) != 2 {
				errs = append(errs, fmt.Sprintf(
					"%s plugin: invalid pin value '%s' for pin '%s' on device '%s' (expected format: '<direction> <channel>', e.g. '0 1')",
					pluginName, value, pinName, device))
				continue
			}
			direction, err := strconv.Atoi(parts[0])
			if err != nil || direction < 0 || direction > 2 {
				errs = append(errs, fmt.Sprintf(
					"%s plugin: invalid direction '%s' for pin '%s' on device '%s' (must be 0, 1, or 2)",
					pluginName, parts[0], pinName, device))
			}
			channel, err := strconv.Atoi(parts[1])
			if err != nil || channel < 0 {
				errs = append(errs, fmt.Sprintf(
					"%s plugin: invalid channel '%s' for pin '%s' on device '%s' (must be a non-negative integer)",
					pluginName, parts[1], pinName, device))
			}
		}
	}
	return errs
}

// validateInterconnections checks that interconnections entries have valid fields
func validateInterconnections(inputs []PhaseInputs) []string {
	var errs []string
	validParts := validPartNames()
	validConns := validConnectorNames()

	for i, card := range inputs {
		// Validate ID is not empty
		if card.ID == "" {
			errs = append(errs, fmt.Sprintf(
				"interconnections[%d]: 'id' field is required", i))
		}

		// Validate Part name
		if card.Part == "" {
			errs = append(errs, fmt.Sprintf(
				"interconnections[%d] (%s): 'Part' field is required", i, card.ID))
		} else {
			found := false
			for _, p := range validParts {
				if p == card.Part {
					found = true
					break
				}
			}
			if !found {
				errs = append(errs, fmt.Sprintf(
					"interconnections[%d] (%s): unknown Part '%s' (valid parts: %s)",
					i, card.ID, card.Part, strings.Join(validParts, ", ")))
			}
		}

		// Validate input connector name (if not GNSS input and not T-BC upstream)
		if !card.GnssInput && card.UpstreamPort == "" && card.Input.Connector != "" {
			if !isValidConnector(card.Input.Connector, validConns) {
				errs = append(errs, fmt.Sprintf(
					"interconnections[%d] (%s): unknown input connector '%s' (valid connectors: %s)",
					i, card.ID, card.Input.Connector, strings.Join(validConns, ", ")))
			}
		}

		// Validate that non-leading, non-GNSS cards have an input connector
		if !card.GnssInput && card.UpstreamPort == "" && card.Input.Connector == "" {
			errs = append(errs, fmt.Sprintf(
				"interconnections[%d] (%s): must specify either 'gnssInput: true', 'upstreamPort', or 'inputConnector'",
				i, card.ID))
		}

		// Validate phaseOutputConnectors
		for j, conn := range card.PhaseOutputConnectors {
			if !isValidConnector(conn, validConns) {
				errs = append(errs, fmt.Sprintf(
					"interconnections[%d] (%s): unknown phaseOutputConnector[%d] '%s' (valid connectors: %s)",
					i, card.ID, j, conn, strings.Join(validConns, ", ")))
			}
		}
	}
	return errs
}

func isValidConnector(name string, validConns []string) bool {
	lower := strings.ToLower(name)
	for _, c := range validConns {
		if c == lower {
			return true
		}
	}
	return false
}

// ValidateE810Opts validates the E810 plugin options and returns all detected issues
func ValidateE810Opts(rawJSON []byte) []string {
	var errs []string

	// Check for unknown top-level fields
	var check E810Opts
	if fieldErrs := validateUnknownFields(rawJSON, &check); len(fieldErrs) > 0 {
		errs = append(errs, fieldErrs...)
	}

	// Parse normally to validate field values
	var opts E810Opts
	if err := json.Unmarshal(rawJSON, &opts); err != nil {
		errs = append(errs, fmt.Sprintf("e810: failed to parse configuration: %s", err))
		return errs
	}

	errs = append(errs, validatePinNames(opts.DevicePins, "e810", hasSysfsSMAPinsFunc)...)
	errs = append(errs, validatePinValues(opts.DevicePins, "e810")...)

	// Validate interconnections
	if opts.PhaseInputs != nil {
		errs = append(errs, validateInterconnections(opts.PhaseInputs)...)
	}

	return errs
}

// ValidateE825Opts validates the E825 plugin options and returns all detected issues
func ValidateE825Opts(rawJSON []byte) []string {
	var errs []string

	// Check for unknown top-level fields
	var check E825Opts
	if fieldErrs := validateUnknownFields(rawJSON, &check); len(fieldErrs) > 0 {
		errs = append(errs, fieldErrs...)
	}

	// Parse normally to validate field values
	var opts E825Opts
	if err := json.Unmarshal(rawJSON, &opts); err != nil {
		errs = append(errs, fmt.Sprintf("e825: failed to parse configuration: %s", err))
		return errs
	}

	errs = append(errs, validatePinNames(opts.DevicePins, "e825", hasSysfsSMAPinsFunc)...)
	errs = append(errs, validatePinValues(opts.DevicePins, "e825")...)

	return errs
}

// ValidateE830Opts validates the E830 plugin options and returns all detected issues
func ValidateE830Opts(rawJSON []byte) []string {
	var errs []string

	// Check for unknown top-level fields
	var check E830Opts
	if fieldErrs := validateUnknownFields(rawJSON, &check); len(fieldErrs) > 0 {
		errs = append(errs, fieldErrs...)
	}

	// Parse normally to validate field values
	var opts E830Opts
	if err := json.Unmarshal(rawJSON, &opts); err != nil {
		errs = append(errs, fmt.Sprintf("e830: failed to parse configuration: %s", err))
		return errs
	}

	errs = append(errs, validatePinNames(opts.DevicePins, "e830", hasSysfsSMAPinsFunc)...)
	errs = append(errs, validatePinValues(opts.DevicePins, "e830")...)

	return errs
}
