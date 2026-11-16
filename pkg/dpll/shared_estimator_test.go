package dpll

import (
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/require"
)

type holdoverModelFunc func(time.Duration) (int64, error)

func (model holdoverModelFunc) Estimate(elapsed time.Duration) (int64, error) {
	return model(elapsed)
}

func newEstimatorDPLL(t *testing.T, mode event.ClockType, options ...Option) *DpllConfig {
	t.Helper()
	source := event.GNSS
	if mode == event.TBC {
		source = event.PTP4l
	}
	dpll := NewDpll(1, 100, 100, 40, "estimator-test", []event.EventSource{source}, MOCK, nil,
		0, 0, 0, config.ProcessConfig{ClockType: mode, EventChannel: make(chan event.Event, 64)}, nil, options...)
	dpll.phaseOffset = 3
	dpll.inSpec = true
	dpll.state = event.PTP_HOLDOVER
	t.Cleanup(dpll.ticker.Stop)
	return dpll
}

func TestConstructorEstimatorDrivesBothHoldoverWorkers(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			var calls atomic.Int32
			option, err := WithHoldoverModel(holdoverModelFunc(func(elapsed time.Duration) (int64, error) {
				calls.Add(1)
				return 50 + int64(elapsed.Seconds())%3, nil
			}))
			require.NoError(t, err)
			dpll := newEstimatorDPLL(t, mode, option)
			events := make(chan event.Event, 64)
			dpll.processConfig.EventChannel = events
			dpll.MaxInSpecOffset = 100
			dpll.phaseStatus, dpll.frequencyStatus = DPLL_LOCKED_HO_ACQ, DPLL_LOCKED_HO_ACQ
			dpll.stateDecision()
			require.Zero(t, calls.Load(), "locked hardware must not evaluate a model")
			dpll.state = event.PTP_HOLDOVER
			dpll.onHoldover = true
			dpll.holdoverCloseCh = make(chan bool)
			done := make(chan struct{})
			go func() {
				dpll.holdover()
				close(done)
			}()
			t.Cleanup(func() {
				select {
				case dpll.holdoverCloseCh <- true:
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("holdover worker did not accept cancellation")
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("holdover worker did not terminate")
				}
				require.False(t, dpll.onHoldover)
			})
			deadline := time.NewTimer(3 * time.Second)
			defer deadline.Stop()
			for {
				select {
				case sample := <-events:
					data := sample.Data.(*event.DPLLData)
					if data.Offset == nil || *data.Offset < 50 {
						continue
					}
					require.Equal(t, event.PTP_HOLDOVER, data.State)
					require.False(t, data.OutOfSpec)
					require.Positive(t, calls.Load())
					return
				case <-deadline.C:
					t.Fatal("actual holdover worker did not publish the injected nonlinear estimate")
				}
			}
		})
	}
}

func TestSharedDefaultEstimateRoundingAndModePolicy(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		for _, sample := range []struct {
			elapsed time.Duration
			offset  int64
		}{{1499 * time.Millisecond, 1}, {1500 * time.Millisecond, 2}, {40 * time.Second, 40}, {41 * time.Second, 41}} {
			dpll := newEstimatorDPLL(t, mode)
			terminal := dpll.advanceHoldover(sample.elapsed)
			if sample.offset > 40 && mode == event.GM {
				require.True(t, terminal)
				require.Equal(t, event.PTP_FREERUN, dpll.state)
				require.Equal(t, int64(FaultyPhaseOffset), dpll.phaseOffset)
			} else {
				require.False(t, terminal)
				require.Equal(t, event.PTP_HOLDOVER, dpll.state)
				require.Equal(t, sample.offset, dpll.phaseOffset)
			}
			require.Equal(t, sample.offset <= 40, dpll.inSpec)
		}
	}
}

func TestSharedEstimatorInjectionValidation(t *testing.T) {
	var typedNil *linearHoldoverModel
	var nilFunction holdoverModelFunc
	for _, model := range []HoldoverModel{nil, typedNil, nilFunction} {
		_, err := WithHoldoverModel(model)
		require.Error(t, err)
	}
	option, err := WithHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) { return 59, nil }))
	require.NoError(t, err)
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		dpll := newEstimatorDPLL(t, mode, nil, option)
		dpll.MaxInSpecOffset = 100
		require.False(t, dpll.advanceHoldover(3*time.Second))
		require.Equal(t, int64(59), dpll.phaseOffset)
	}
}

func TestLegacyEstimatorErrorsAndUnsignedLimits(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		for _, model := range []HoldoverModel{
			holdoverModelFunc(func(time.Duration) (int64, error) { return 0, errors.New("estimate failed") }),
			holdoverModelFunc(func(time.Duration) (int64, error) { return -1, nil }),
			defaultHoldoverModel(math.MaxUint64, 1),
		} {
			option, err := WithHoldoverModel(model)
			require.NoError(t, err)
			dpll := newEstimatorDPLL(t, mode, option)
			require.True(t, dpll.advanceHoldover(time.Second))
			require.Equal(t, event.PTP_FREERUN, dpll.state)
			require.False(t, dpll.inSpec)
		}
		zeroDrift := newEstimatorDPLL(t, mode, func(d *DpllConfig) { d.LocalMaxHoldoverOffSet = 0 })
		require.False(t, zeroDrift.advanceHoldover(time.Second))
		require.Zero(t, zeroDrift.phaseOffset)
		negativeElapsed := newEstimatorDPLL(t, mode)
		require.True(t, negativeElapsed.advanceHoldover(-time.Second))
	}
}

func TestSharedEstimatorLegacyTimeoutAndAbsentOffset(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		var calls atomic.Int32
		option, err := WithHoldoverModel(holdoverModelFunc(func(time.Duration) (int64, error) {
			calls.Add(1)
			return 59, nil
		}))
		require.NoError(t, err)
		disabled := newEstimatorDPLL(t, mode, option)
		disabled.LocalHoldoverTimeout = 0
		disabled.onHoldover = true
		disabled.holdover()
		require.Zero(t, calls.Load())
		require.Equal(t, event.PTP_FREERUN, disabled.state)
		require.False(t, disabled.inSpec)
		require.False(t, disabled.onHoldover)
		require.Equal(t, int64(FaultyPhaseOffset), disabled.phaseOffset)
		noOffset := newEstimatorDPLL(t, mode, option)
		events := make(chan event.Event, 64)
		noOffset.processConfig.EventChannel = events
		noOffset.flags = FlagNoPhaseOffset
		require.False(t, noOffset.advanceHoldover(time.Second))
		data := (<-events).Data.(*event.DPLLData)
		require.Nil(t, data.Offset)
		require.False(t, data.OutOfSpec)
	}
}
