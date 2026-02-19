package hardwareconfig

// RemapDefaultProfileLabels applies board label remapping to HardwareDefaults
func RemapDefaultProfileLabels(hd *HardwareDefaults, labelMap BoardLabelMap) {
	if hd == nil || len(labelMap) == 0 {
		return
	}

	RemapPinDefaults(hd, labelMap)
	RemapDelayCompensation(hd, labelMap)
}

// RemapPinDefaults remaps PinDefaults map keys
func RemapPinDefaults(hd *HardwareDefaults, labelMap BoardLabelMap) {
	if hd == nil || hd.PinDefaults == nil || len(labelMap) == 0 {
		return
	}

	remapped := make(map[string]*PinDefault)
	for oldKey, pinDefault := range hd.PinDefaults {
		newKey := applyLabelRemap(oldKey, labelMap)
		remapped[newKey] = pinDefault
	}
	hd.PinDefaults = remapped
}

// RemapDelayCompensation remaps DelayCompensation CompensationPoint.Name values
func RemapDelayCompensation(hd *HardwareDefaults, labelMap BoardLabelMap) {
	if hd == nil || hd.DelayCompensation == nil || len(labelMap) == 0 {
		return
	}

	for i := range hd.DelayCompensation.Components {
		comp := &hd.DelayCompensation.Components[i]
		if comp.CompensationPoint != nil {
			comp.CompensationPoint.Name = applyLabelRemap(comp.CompensationPoint.Name, labelMap)
		}
	}
}

// RemapBehaviorProfileLabels applies board label remapping to BehaviorProfileTemplate
// Modifies the struct in-place for performance (remapping happens once before caching)
func RemapBehaviorProfileLabels(profile *BehaviorProfileTemplate, labelMap BoardLabelMap) {
	if profile == nil || len(labelMap) == 0 {
		return
	}

	// Remap PinRoles values
	if profile.PinRoles != nil {
		for role, oldLabel := range profile.PinRoles {
			profile.PinRoles[role] = applyLabelRemap(oldLabel, labelMap)
		}
	}
}

// applyLabelRemap applies label remapping to a single string
// Returns the remapped value if found, otherwise returns the original string
func applyLabelRemap(s string, labelMap BoardLabelMap) string {
	if newLabel, ok := labelMap[s]; ok {
		return newLabel
	}
	return s
}
