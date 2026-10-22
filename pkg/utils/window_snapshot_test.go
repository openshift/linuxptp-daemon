package utils_test

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWindowSnapshot(t *testing.T) {
	w := utils.NewWindow(3)
	empty := w.Snapshot()
	assert.True(t, empty.IsEmpty())
	w.Insert(1)
	w.Insert(2)
	partial := w.Snapshot()
	assert.False(t, partial.IsFull())
	assert.Equal(t, 1.5, partial.Mean())
	w.Insert(3)
	w.Insert(4)
	full := w.Snapshot()
	assert.True(t, full.IsFull())
	assert.Equal(t, float64(4), full.LastInserted())
	assert.Equal(t, 3, full.CountSamples(func(v float64) bool { return v >= 2 }))
	require.NoError(t, w.SetWeights([]float64{1, 2, 3}))
	w.Insert(5)
	assert.True(t, empty.IsEmpty(), "later samples do not populate an old snapshot")
	assert.Equal(t, float64(2), partial.LastInserted())
	assert.Equal(t, 1.5, partial.Mean())
	assert.Equal(t, float64(4), full.LastInserted())
	assert.Equal(t, float64(3), full.Mean(), "weights and samples are detached")
}
