package intel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"
)

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

// connectorNamesForPart returns valid connector names for a specific hardware part.
func connectorNamesForPart(part string) []string {
	specYaml, ok := hardware[part]
	if !ok {
		return nil
	}
	delays := InternalDelays{}
	if err := yaml.Unmarshal([]byte(specYaml), &delays); err != nil {
		return nil
	}
	connectors := map[string]bool{}
	for _, link := range delays.ExternalInputs {
		connectors[strings.ToLower(link.Connector)] = true
	}
	for _, link := range delays.ExternalOutputs {
		connectors[strings.ToLower(link.Connector)] = true
	}
	if delays.GnssInput.Connector != "" {
		connectors[strings.ToLower(delays.GnssInput.Connector)] = true
	}
	result := make([]string, 0, len(connectors))
	for c := range connectors {
		result = append(result, c)
	}
	return result
}

// isValidPart checks whether a part name exists in the hardware map.
func isValidPart(part string) bool {
	_, ok := hardware[part]
	return ok
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

// validateDevicePins checks pin names and values in a single pass per device.
func validateDevicePins(devicePins map[string]pinSet, pluginName string) []string {
	var errs []string
	for device, pins := range devicePins {
		useSysfs := pluginName != "e810" || hasSysfsSMAPins(device)

		var phcName string
		if useSysfs {
			deviceDir := fmt.Sprintf("/sys/class/net/%s/device/ptp/", device)
			phcs, err := filesystem.ReadDir(deviceDir)
			if err != nil || len(phcs) == 0 {
				errs = append(errs, fmt.Sprintf(
					"%s plugin: cannot discover sysfs pins for device '%s': %v",
					pluginName, device, err))
				continue
			}
			phcName = phcs[0].Name()
		}

		for pinName, value := range pins {
			if useSysfs {
				pinPath := fmt.Sprintf("/sys/class/net/%s/device/ptp/%s/pins/%s", device, phcName, pinName)
				if _, err := filesystem.ReadFile(pinPath); err != nil {
					errs = append(errs, fmt.Sprintf(
						"%s plugin: unknown pin name '%s' for device '%s'",
						pluginName, pinName, device))
				}
			} else {
				if len(DpllPins.GetAllPinsByLabel(pinName)) == 0 {
					errs = append(errs, fmt.Sprintf(
						"%s plugin: unknown pin name '%s' for device '%s' (not found in DPLL pin labels)",
						pluginName, pinName, device))
				}
			}

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

	for i, card := range inputs {
		if card.ID == "" {
			errs = append(errs, fmt.Sprintf(
				"interconnections[%d]: 'id' field is required", i))
		}

		if card.Part == "" {
			errs = append(errs, fmt.Sprintf(
				"interconnections[%d] (%s): 'Part' field is required", i, card.ID))
			continue
		}

		if !isValidPart(card.Part) {
			errs = append(errs, fmt.Sprintf(
				"interconnections[%d] (%s): unknown Part '%s'",
				i, card.ID, card.Part))
			continue
		}

		partConns := connectorNamesForPart(card.Part)

		if !card.GnssInput && card.UpstreamPort == "" && card.Input.Connector != "" {
			if !isValidConnector(card.Input.Connector, partConns) {
				errs = append(errs, fmt.Sprintf(
					"interconnections[%d] (%s): unknown input connector '%s' for part '%s' (valid connectors: %s)",
					i, card.ID, card.Input.Connector, card.Part, strings.Join(partConns, ", ")))
			}
		}

		if !card.GnssInput && card.UpstreamPort == "" && card.Input.Connector == "" {
			errs = append(errs, fmt.Sprintf(
				"interconnections[%d] (%s): must specify either 'gnssInput: true', 'upstreamPort', or 'inputConnector'",
				i, card.ID))
		}

		for j, conn := range card.PhaseOutputConnectors {
			if !isValidConnector(conn, partConns) {
				errs = append(errs, fmt.Sprintf(
					"interconnections[%d] (%s): unknown phaseOutputConnector[%d] '%s' for part '%s' (valid connectors: %s)",
					i, card.ID, j, conn, card.Part, strings.Join(partConns, ", ")))
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

	errs = append(errs, validateDevicePins(opts.DevicePins, "e810")...)

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

	errs = append(errs, validateDevicePins(opts.DevicePins, "e825")...)

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

	errs = append(errs, validateDevicePins(opts.DevicePins, "e830")...)

	return errs
}
