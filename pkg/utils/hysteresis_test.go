package utils

import (
	"testing"
)

func TestHysteresisFilterConfig(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 2, 2)
	if f.InSyncThreshold() != 50 {
		t.Fatalf("inSyncThreshold=%v, want 50", f.InSyncThreshold())
	}
	if f.OutOfSyncThreshold() != 100 {
		t.Fatalf("outOfSyncThreshold=%v, want 100", f.OutOfSyncThreshold())
	}
	if f.InSyncSamplesRequired() != 2 {
		t.Fatalf("inSyncSamplesRequired=%d, want 2", f.InSyncSamplesRequired())
	}
	if f.State() != HysteresisUnknown {
		t.Fatalf("state=%v, want Unknown", f.State())
	}
	f.Config(60, 120, 3, 3)
	if f.InSyncThreshold() != 60 || f.OutOfSyncThreshold() != 120 || f.InSyncSamplesRequired() != 3 {
		t.Fatalf("Config did not update: in=%v out=%v samples=%d",
			f.InSyncThreshold(), f.OutOfSyncThreshold(), f.InSyncSamplesRequired())
	}
}

func TestHysteresisFilterLowAfterConsecutive(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 2, 2)
	state, _ := f.Update(25)
	if state != HysteresisUnknown {
		t.Fatalf("first sample state=%v, want Unknown (needs 2 consecutive)", state)
	}
	state, _ = f.Update(25)
	if state != HysteresisLow {
		t.Fatalf("second sample state=%v, want Low", state)
	}
	state, _ = f.Update(25)
	if state != HysteresisLow {
		t.Fatalf("third sample state=%v, want Low (stable)", state)
	}
}

func TestHysteresisFilterHighAfterConsecutive(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 2, 2)
	state, _ := f.Update(150)
	if state != HysteresisUnknown {
		t.Fatalf("first sample state=%v, want Unknown (needs 2 consecutive)", state)
	}
	state, _ = f.Update(150)
	if state != HysteresisHigh {
		t.Fatalf("second sample state=%v, want High", state)
	}
}

func TestHysteresisFilterBandResetsCounters(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 3, 3)
	_, changed := f.Update(150)
	if changed {
		t.Fatalf("unexpected transition on first sample")
	}
	// A band sample resets the out-of-sync counter.
	f.Update(75)
	f.Update(150)
	f.Update(150)
	state, changed := f.Update(150)
	if state != HysteresisHigh || !changed {
		t.Fatalf("state=%v changed=%v, want High only after full run post-band-reset", state, changed)
	}
}

func TestHysteresisFilterTransitionChangedFlag(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 1, 1)
	for _, tc := range []struct {
		value   float64
		want    HysteresisState
		wantChg bool
	}{
		{25, HysteresisLow, true},   // Unknown -> Low
		{25, HysteresisLow, false},  // stable
		{150, HysteresisHigh, true}, // Low -> High
		{150, HysteresisHigh, false},
	} {
		state, changed := f.Update(tc.value)
		if state != tc.want || changed != tc.wantChg {
			t.Fatalf("value=%v: state=%v changed=%v, want state=%v changed=%v",
				tc.value, state, changed, tc.want, tc.wantChg)
		}
	}
}

func TestHysteresisFilterReset(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 1, 1)
	f.Update(25)
	if f.State() != HysteresisLow {
		t.Fatalf("state=%v, want Low before reset", f.State())
	}
	f.Reset()
	if f.State() != HysteresisUnknown {
		t.Fatalf("state=%v, want Unknown after reset", f.State())
	}
	if f.InSyncThreshold() != 50 || f.OutOfSyncThreshold() != 100 || f.InSyncSamplesRequired() != 1 {
		t.Fatalf("Reset changed thresholds: in=%v out=%v samples=%d",
			f.InSyncThreshold(), f.OutOfSyncThreshold(), f.InSyncSamplesRequired())
	}
}

// TestHysteresisFilterConfigClampsSamples verifies Config clamps sample counts
// below 1 to 1, so a zero count cannot degenerate the filter into an instant
// state.
func TestHysteresisFilterConfigClampsSamples(t *testing.T) {
	f := NewHysteresisFilter(50, 100, 0, -3)
	if f.InSyncSamplesRequired() != 1 || f.OutOfSyncSamplesRequired() != 1 {
		t.Fatalf("sample counts=%d/%d, want 1/1",
			f.InSyncSamplesRequired(), f.OutOfSyncSamplesRequired())
	}
	// One sample in each direction is enough to transition.
	if state, _ := f.Update(25); state != HysteresisLow {
		t.Fatalf("state=%v, want Low after one in-sync sample", state)
	}
	if state, _ := f.Update(150); state != HysteresisHigh {
		t.Fatalf("state=%v, want High after one out-of-sync sample", state)
	}
}

// TestHysteresisFilterReusable verifies instances are independent.
func TestHysteresisFilterReusable(t *testing.T) {
	osClock := NewHysteresisFilter(50, 100, 2, 2)
	other := NewHysteresisFilter(200, 500, 5, 5)
	osClock.Update(25)
	osClock.Update(25)
	if osClock.State() != HysteresisLow {
		t.Fatalf("osClock state=%v, want Low", osClock.State())
	}
	if other.State() != HysteresisUnknown {
		t.Fatalf("other state=%v, want Unknown (instances independent)", other.State())
	}
}

// TestHysteresisFilterAsymmetricCounts verifies the per-direction sample
// counts: with outOfSyncSamples=1 a single breach revokes Low immediately,
// while re-entering Low still requires the full in-sync streak.
func TestHysteresisFilterAsymmetricCounts(t *testing.T) {
	f := NewHysteresisFilter(50, 50, 3, 1)

	f.Update(10)
	f.Update(20)
	if f.State() != HysteresisUnknown {
		t.Fatalf("state=%v, want Unknown before 3 consecutive in-sync samples", f.State())
	}
	f.Update(30)
	if f.State() != HysteresisLow {
		t.Fatalf("state=%v, want Low after 3 consecutive in-sync samples", f.State())
	}

	// One breach immediately revokes Low.
	state, changed := f.Update(75)
	if state != HysteresisHigh || !changed {
		t.Fatalf("state=%v changed=%v, want High immediately on a single breach", state, changed)
	}

	// Re-entering Low needs the full streak again.
	f.Update(10)
	f.Update(20)
	if f.State() == HysteresisLow {
		t.Fatalf("state=%v, Low must require the full in-sync streak again", f.State())
	}
	f.Update(30)
	if f.State() != HysteresisLow {
		t.Fatalf("state=%v, want Low after a fresh full streak", f.State())
	}
}
