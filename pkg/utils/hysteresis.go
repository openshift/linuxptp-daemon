package utils

import (
	"math"
)

// HysteresisState is the state a HysteresisFilter emits after classifying
// deviation samples.
type HysteresisState int

const (
	// HysteresisUnknown is the initial state of a fresh filter.
	HysteresisUnknown HysteresisState = iota
	// HysteresisLow means abs(deviation) <= inSyncThreshold for the required
	// consecutive in-sync samples.
	HysteresisLow
	// HysteresisHigh means abs(deviation) > outOfSyncThreshold for the required
	// consecutive out-of-sync samples.
	HysteresisHigh
)

// HysteresisFilter classifies scalar deviation samples against two thresholds,
// flipping between Low and High only after the required consecutive samples
// crossed the matching threshold, so a single jitter sample does not flap the
// state. Samples inside the hysteresis band (between the thresholds) reset both
// consecutive-sample counters. The required sample count is configured per
// direction, so a consumer can revoke a state on a single breach
// (outOfSyncSamples = 1) while still debouncing the other direction. The filter
// is generic and depends only on the standard library.
type HysteresisFilter struct {
	inSyncThreshold          float64
	outOfSyncThreshold       float64
	inSyncSamplesRequired    int
	outOfSyncSamplesRequired int
	lastState                HysteresisState
	inSyncCount              int
	outOfSyncCount           int
}

// NewHysteresisFilter returns a filter with the given thresholds (in the
// deviation's units) and required consecutive samples per direction, starting
// in Unknown.
func NewHysteresisFilter(inSyncThreshold, outOfSyncThreshold float64, inSyncSamples, outOfSyncSamples int) *HysteresisFilter {
	f := &HysteresisFilter{}
	f.Config(inSyncThreshold, outOfSyncThreshold, inSyncSamples, outOfSyncSamples)
	return f
}

// Config sets the thresholds and the required consecutive samples per
// direction, resetting the counters but keeping the current state, so a running
// filter can be reconfigured without losing it. Sample counts below 1 are
// clamped to 1.
func (f *HysteresisFilter) Config(inSyncThreshold, outOfSyncThreshold float64, inSyncSamples, outOfSyncSamples int) {
	f.inSyncThreshold = inSyncThreshold
	f.outOfSyncThreshold = outOfSyncThreshold
	f.inSyncSamplesRequired = max(inSyncSamples, 1)
	f.outOfSyncSamplesRequired = max(outOfSyncSamples, 1)
	f.inSyncCount, f.outOfSyncCount = 0, 0
}

// Reset clears the counters and returns the filter to the Unknown state,
// keeping the configuration.
func (f *HysteresisFilter) Reset() {
	f.inSyncCount, f.outOfSyncCount = 0, 0
	f.lastState = HysteresisUnknown
}

// Update feeds one deviation sample, returning the resulting state and whether
// it transitioned.
func (f *HysteresisFilter) Update(rawDeviation float64) (HysteresisState, bool) {
	absValue := math.Abs(rawDeviation)
	switch {
	case absValue <= f.inSyncThreshold:
		f.outOfSyncCount = 0
		f.inSyncCount++
	case absValue > f.outOfSyncThreshold:
		f.inSyncCount = 0
		f.outOfSyncCount++
	default: // hysteresis band: reset both counters
		f.inSyncCount, f.outOfSyncCount = 0, 0
	}
	next := f.lastState
	if f.inSyncCount >= f.inSyncSamplesRequired {
		next = HysteresisLow
	} else if f.outOfSyncCount >= f.outOfSyncSamplesRequired {
		next = HysteresisHigh
	}
	changed := next != f.lastState
	f.lastState = next
	return next, changed
}

// InSyncThreshold returns the configured in-sync threshold.
func (f *HysteresisFilter) InSyncThreshold() float64 {
	return f.inSyncThreshold
}

// OutOfSyncThreshold returns the configured out-of-sync threshold.
func (f *HysteresisFilter) OutOfSyncThreshold() float64 {
	return f.outOfSyncThreshold
}

// InSyncSamplesRequired returns the configured number of consecutive in-sync
// samples required for a transition to Low.
func (f *HysteresisFilter) InSyncSamplesRequired() int {
	return f.inSyncSamplesRequired
}

// OutOfSyncSamplesRequired returns the configured number of consecutive
// out-of-sync samples required for a transition to High.
func (f *HysteresisFilter) OutOfSyncSamplesRequired() int {
	return f.outOfSyncSamplesRequired
}

// Samples returns the consecutive run length, or zero after a band sample/reset.
func (f *HysteresisFilter) Samples() int { return max(f.inSyncCount, f.outOfSyncCount) }

// State returns the filter's current state.
func (f *HysteresisFilter) State() HysteresisState {
	return f.lastState
}
