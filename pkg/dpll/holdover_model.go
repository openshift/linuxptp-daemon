package dpll

import (
	"fmt"
	"math"
	"reflect"
	"time"
)

// HoldoverModel estimates nonnegative nanosecond error from episode elapsed time.
type HoldoverModel interface {
	Estimate(elapsed time.Duration) (offsetNS int64, err error)
}

// Option configures a DPLL instance before it starts monitoring.
type Option func(*DpllConfig)

// WithHoldoverModel supplies an immutable estimator to a DPLL holdover worker.
func WithHoldoverModel(model HoldoverModel) (Option, error) {
	if err := validateHoldoverModel(model); err != nil {
		return nil, err
	}
	return func(d *DpllConfig) { d.holdoverModel = model }, nil
}

func validateHoldoverModel(model HoldoverModel) error {
	if model == nil {
		return fmt.Errorf("nil holdover model")
	}
	value := reflect.ValueOf(model)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return fmt.Errorf("nil holdover model")
		}
	}
	return nil
}

type linearHoldoverModel struct {
	slope float64
}

func defaultHoldoverModel(maxOffsetNS, timeoutSeconds uint64) linearHoldoverModel {
	if timeoutSeconds == 0 {
		return linearHoldoverModel{}
	}
	return linearHoldoverModel{slope: float64(maxOffsetNS) / float64(timeoutSeconds)}
}

// NewLinearHoldoverModel creates a bounded linear estimate from positive limits.
func NewLinearHoldoverModel(maxOffsetNS uint64, timeout time.Duration) (HoldoverModel, error) {
	if maxOffsetNS == 0 || timeout <= 0 {
		return nil, fmt.Errorf("linear holdover requires positive offset and timeout")
	}
	return linearHoldoverModel{slope: float64(maxOffsetNS) / timeout.Seconds()}, nil
}

func (model linearHoldoverModel) Estimate(elapsed time.Duration) (int64, error) {
	if elapsed < 0 {
		return 0, fmt.Errorf("negative holdover elapsed time: %s", elapsed)
	}
	offset := math.Round(model.slope * elapsed.Seconds())
	if math.IsNaN(offset) || math.IsInf(offset, 0) || offset >= float64(math.MaxInt64) {
		return 0, fmt.Errorf("holdover estimate is not representable in nanoseconds")
	}
	return int64(offset), nil
}
