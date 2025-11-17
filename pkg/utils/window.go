package utils

import (
	"fmt"
	"math"

	"gonum.org/v1/gonum/stat"
)

// Window represents a sliding window of float64 values with associated weights.
// It supports various statistical operations on the windowed data.
type Window struct {
	data      []float64
	weights   []float64
	size      int
	nextIndex int
	full      bool
}

// NewWindow creates a new Window with the specified size.
// All weights are initialized to 1.0.
func NewWindow(size int) *Window {
	weights := make([]float64, size)
	for i := 0; i < size; i++ {
		weights[i] = 1
	}
	return &Window{
		data:    make([]float64, size),
		weights: weights,
		size:    size,
	}
}

// getData returns the current data in the window.
// If the window is not full, it returns only the populated portion.
func (w *Window) getData() []float64 {
	if w.full {
		return w.data
	}
	return w.data[:w.nextIndex]
}

// getWeights returns the current weights in the window.
// If the window is not full, it returns only the populated portion.
func (w *Window) getWeights() []float64 {
	if w.full {
		return w.weights
	}
	return w.weights[:w.nextIndex]
}

// AbsMax returns the maximum absolute value in the window.
func (w *Window) AbsMax() float64 {
	var m float64
	for _, f := range w.getData() {
		m = math.Max(m, math.Abs(f))
	}
	return m
}

// Max returns the maximum value in the window.
func (w *Window) Max() float64 {
	var m float64
	for _, f := range w.getData() {
		m = math.Max(m, f)
	}
	return m
}

// AbsMin returns the minimum absolute value in the window.
func (w *Window) AbsMin() float64 {
	var m float64
	for _, f := range w.getData() {
		m = math.Min(m, math.Abs(f))
	}
	return m
}

// Min returns the minimum value in the window.
func (w *Window) Min() float64 {
	var m float64
	for _, f := range w.getData() {
		m = math.Min(m, f)
	}
	return m
}

// AbsMean returns the weighted mean of the absolute values in the window.
func (w *Window) AbsMean() float64 {
	absValues := make([]float64, 0)
	for _, v := range w.getData() {
		absValues = append(absValues, math.Abs(v))
	}
	return stat.Mean(absValues, w.getWeights())
}

// Mean returns the weighted mean of the values in the window.
func (w *Window) Mean() float64 {
	return stat.Mean(w.getData(), w.getWeights())
}

// Median returns the weighted median (50th percentile) of the values in the window.
func (w *Window) Median() float64 {
	return stat.Quantile(0.5, stat.Empirical, w.getData(), w.getWeights())
}

// Variance returns the weighted variance of the values in the window.
func (w *Window) Variance() float64 {
	return stat.Variance(w.getData(), w.getWeights())
}

// StdDev returns the weighted standard deviation of the values in the window.
func (w *Window) StdDev() float64 {
	return stat.StdDev(w.getData(), w.getWeights())
}

// Insert adds a new value to the window.
// The window operates as a circular buffer, overwriting the oldest value when full.
func (w *Window) Insert(v float64) {
	w.data[w.nextIndex] = v
	w.nextIndex = (w.nextIndex + 1) % w.size
	if !w.full && w.nextIndex == 0 {
		w.full = true
	}
}

// LastInserted returns the last value inserted into the window
func (w *Window) LastInserted() float64 {
	lastIndex := w.nextIndex - 1
	if lastIndex < 0 {
		lastIndex = w.size - 1
	}
	return w.data[lastIndex]
}

// SetWeights sets the weights for the window values.
// Returns an error if the weights slice is larger than the window size.
func (w *Window) SetWeights(weights []float64) error {
	if len(weights) > w.size {
		return fmt.Errorf(
			"weights of wrong size: expected=%d actual=%d",
			w.size, len(weights))
	}
	w.weights = weights
	return nil
}
