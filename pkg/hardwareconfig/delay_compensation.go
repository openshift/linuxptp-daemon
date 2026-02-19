package hardwareconfig

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

// ResolvePhaseAdjustments calculates phase adjustments from delay compensation model
// and returns a map of pin board labels to their phase adjustment values.
func ResolvePhaseAdjustments(
	hwDefaults *HardwareDefaults,
	networkInterface string,
) (map[string]int64, error) {
	if hwDefaults == nil || hwDefaults.DelayCompensation == nil {
		return nil, nil
	}

	model := hwDefaults.DelayCompensation
	adjustments := make(map[string]int64)

	// Process each route to calculate compensation
	for _, route := range model.Routes {
		// Only process routes where compensator is a DPLL pin
		compensatorComp := findComponentByID(model.Components, route.Compensator)
		if compensatorComp == nil || compensatorComp.CompensationPoint == nil {
			continue
		}
		if compensatorComp.CompensationPoint.Type != "dpll" {
			continue
		}

		pinLabel := compensatorComp.CompensationPoint.Name
		if pinLabel == "" {
			glog.Warningf("Route %s: compensator %s has no pin name", route.Name, route.Compensator)
			continue
		}

		// Calculate total delay along the route sequence
		totalDelay, err := calculateRouteDelay(model, route.Sequence, networkInterface)
		if err != nil {
			glog.Warningf("Route %s: failed to calculate delay: %v", route.Name, err)
			continue
		}

		adjustments[pinLabel] = totalDelay
		glog.V(4).Infof("Route %s: calculated delay %d ps for pin %s", route.Name, totalDelay, pinLabel)
	}

	return adjustments, nil
}

// calculateRouteDelay sums delays along a route sequence
func calculateRouteDelay(
	model *DelayCompensationModel,
	sequence []string,
	networkInterface string,
) (int64, error) {
	if len(sequence) < 2 {
		return 0, fmt.Errorf("route sequence must have at least 2 components")
	}

	var totalDelay int64
	for i := 0; i < len(sequence)-1; i++ {
		from := sequence[i]
		to := sequence[i+1]

		delay, err := getConnectionDelay(model, from, to, networkInterface)
		if err != nil {
			return 0, fmt.Errorf("connection %s -> %s: %w", from, to, err)
		}

		totalDelay += delay
	}

	return totalDelay, nil
}

// getConnectionDelay retrieves delay value for a connection
func getConnectionDelay(
	model *DelayCompensationModel,
	from, to, networkInterface string,
) (int64, error) {
	for _, conn := range model.Connections {
		if conn.From == from && conn.To == to {
			// Prefer FSRead getter if available
			if conn.Getter != nil && conn.Getter.FSRead != nil {
				delay, err := readDelayFromFS(conn.Getter.FSRead.Path, networkInterface)
				if err == nil {
					return delay, nil
				}
				glog.V(4).Infof("Failed to read delay from FS %s: %v, using delayPs", conn.Getter.FSRead.Path, err)
			}
			// Fall back to delayPs
			return int64(conn.DelayPs), nil
		}
	}
	return 0, fmt.Errorf("connection not found: %s -> %s", from, to)
}

// readDelayFromFS reads delay value from sysfs, supporting {interface} placeholder
func readDelayFromFS(path, networkInterface string) (int64, error) {
	// Replace {interface} placeholder if present
	resolvedPath := strings.ReplaceAll(path, "{interface}", networkInterface)
	if resolvedPath == path && networkInterface != "" {
		// Try to find ptp* pattern and replace
		resolvedPath = strings.ReplaceAll(path, "ptp*", "ptp0")
	}

	// TODO: Implement actual file reading when needed
	// For now, return error to fall back to delayPs
	return 0, fmt.Errorf("FSRead not yet implemented, path: %s", resolvedPath)
}

// findComponentByID finds a component by its ID
func findComponentByID(components []Component, id string) *Component {
	for i := range components {
		if components[i].ID == id {
			return &components[i]
		}
	}
	return nil
}

// PopulatePhaseAdjustmentsFromDelays populates PhaseAdjustment fields in HardwareConfig
// based on calculated delays from the delay compensation model.
// Delays from delays.yaml are summed directly and combined with user-specified phaseAdjustment values.
func PopulatePhaseAdjustmentsFromDelays(
	hwConfig *ptpv2alpha1.HardwareConfig,
	hwDefaults *HardwareDefaults,
	networkInterface string,
) error {
	// Calculate internal adjustments from delay compensation model
	internalAdjustments, err := ResolvePhaseAdjustments(hwDefaults, networkInterface)
	if err != nil {
		return fmt.Errorf("failed to resolve phase adjustments: %w", err)
	}
	if len(internalAdjustments) == 0 {
		glog.Infof("No delay compensation adjustments calculated for interface %s", networkInterface)
		return nil
	}

	glog.Infof("Resolved %d phase adjustments: %v", len(internalAdjustments), internalAdjustments)

	cc := hwConfig.Spec.Profile.ClockChain
	for i := range cc.Structure {
		subsystem := &cc.Structure[i]
		// Process PhaseInputs
		if subsystem.DPLL.PhaseInputs != nil {
			glog.Infof("Subsystem %s: checking %d pins in PhaseInputs", subsystem.Name, len(subsystem.DPLL.PhaseInputs))
			for pinLabel := range subsystem.DPLL.PhaseInputs {
				glog.V(2).Infof("  Checking pin %s in PhaseInputs", pinLabel)
				// Check if we have a calculated adjustment for this pin
				if internalAdjustment, ok := internalAdjustments[pinLabel]; ok {
					pinConfig := subsystem.DPLL.PhaseInputs[pinLabel]

					// Get user-specified adjustment (defaults to 0 if not set)
					userAdjustment := int64(0)
					if pinConfig.PhaseAdjustment != nil {
						userAdjustment = *pinConfig.PhaseAdjustment
					}

					// Total adjustment = internal delay (from delays.yaml) + user adjustment
					totalAdjustment := internalAdjustment + userAdjustment

					// Update PhaseAdjustment with the total
					pinConfig.PhaseAdjustment = &totalAdjustment

					// Update the map entry with the modified pinConfig
					subsystem.DPLL.PhaseInputs[pinLabel] = pinConfig

					glog.Infof("Populated phase adjustment for pin %s: Internal delay=%d ps, User adjustment=%d ps, Total=%d ps",
						pinLabel, internalAdjustment, userAdjustment, totalAdjustment)
				}
			}
		}

		glog.Infof("Subsystem %s: checking %d pins in PhaseOutputs", subsystem.Name, len(subsystem.DPLL.PhaseOutputs))
		for pinLabel := range subsystem.DPLL.PhaseOutputs {
			glog.V(2).Infof("  Checking pin %s in PhaseOutputs", pinLabel)
			// Check if we have a calculated adjustment for this pin
			if internalAdjustment, ok := internalAdjustments[pinLabel]; ok {
				pinConfig := subsystem.DPLL.PhaseOutputs[pinLabel]

				// Get user-specified adjustment (defaults to 0 if not set)
				userAdjustment := int64(0)
				if pinConfig.PhaseAdjustment != nil {
					userAdjustment = *pinConfig.PhaseAdjustment
				}

				totalAdjustment := internalAdjustment + userAdjustment

				// Update PhaseAdjustment with the total
				pinConfig.PhaseAdjustment = &totalAdjustment

				// Update the map entry with the modified pinConfig
				subsystem.DPLL.PhaseOutputs[pinLabel] = pinConfig

				glog.Infof("Populated phase adjustment for pin %s: Internal delay=%d ps, User adjustment=%d ps, Total=%d ps",
					pinLabel, internalAdjustment, userAdjustment, totalAdjustment)
			}
		}
	}

	return nil
}
