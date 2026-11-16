package dpll

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLinearHoldoverModelUnitsAndRounding(t *testing.T) {
	t.Parallel()
	model, err := NewLinearHoldoverModel(100, 100*time.Second)
	require.NoError(t, err)
	for _, sample := range []struct {
		elapsed time.Duration
		offset  int64
	}{
		{0, 0}, {1499 * time.Millisecond, 1}, {1500 * time.Millisecond, 2},
		{100 * time.Second, 100}, {100100 * time.Millisecond, 100}, {101 * time.Second, 101},
	} {
		offset, estimateErr := model.Estimate(sample.elapsed)
		require.NoError(t, estimateErr)
		require.Equal(t, sample.offset, offset)
	}
}

func TestLinearHoldoverModelInvalidInputs(t *testing.T) {
	t.Parallel()
	for _, sample := range []struct {
		maxOffset uint64
		timeout   time.Duration
	}{{0, time.Second}, {100, 0}, {100, -time.Second}} {
		_, err := NewLinearHoldoverModel(sample.maxOffset, sample.timeout)
		require.Error(t, err)
	}
	model, err := NewLinearHoldoverModel(math.MaxUint64, time.Nanosecond)
	require.NoError(t, err)
	_, err = model.Estimate(time.Second)
	require.Error(t, err)
	_, err = model.Estimate(-time.Second)
	require.Error(t, err)
}
