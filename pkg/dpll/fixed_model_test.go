package dpll

import (
	"testing"
	"time"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/require"
)

func TestModelSelectedFromResolvedConstructorConfiguration(t *testing.T) {
	for _, mode := range []event.ClockType{event.GM, event.TBC} {
		t.Run(string(mode), func(t *testing.T) {
			source := event.GNSS
			if mode == event.TBC {
				source = event.PTP4l
			}
			resolved := func(d *DpllConfig) {
				d.LocalMaxHoldoverOffSet, d.LocalHoldoverTimeout = 200, 50
			}
			dpll := NewDpll(1, 100, 100, 40, "fixed-model", []event.EventSource{source}, MOCK, nil,
				0, 0, 0, config.ProcessConfig{ClockType: mode}, nil, resolved)
			t.Cleanup(dpll.ticker.Stop)
			require.NotNil(t, dpll.holdoverModel)
			offset, err := dpll.holdoverModel.Estimate(2 * time.Second)
			require.NoError(t, err)
			require.Equal(t, int64(8), offset)
			other := NewDpll(2, 300, 100, 40, "other-model", []event.EventSource{source}, MOCK, nil,
				0, 0, 0, config.ProcessConfig{ClockType: mode}, nil)
			t.Cleanup(other.ticker.Stop)
			offset, err = other.holdoverModel.Estimate(2 * time.Second)
			require.NoError(t, err)
			require.Equal(t, int64(6), offset)
		})
	}
}
