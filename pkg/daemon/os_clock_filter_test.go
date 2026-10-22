package daemon

import (
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/clock"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	parserconstants "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOSClockFilterConfig(t *testing.T) {
	for _, tt := range []struct {
		name      string
		threshold *ptpv1.PtpClockThreshold
		want      clock.OSClockFilterConfig
	}{
		{"defaults", nil, clock.OSClockFilterConfig{InSyncThreshold: 100, OutOfSyncThreshold: 100, Samples: defaultSysOffsetSamples}},
		{"nil fields", &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 100}, clock.OSClockFilterConfig{InSyncThreshold: 100, OutOfSyncThreshold: 100, Samples: defaultSysOffsetSamples}},
		{"custom", &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 100, SysOffsetInSyncThreshold: utils.ToPtr[int64](50), SysOffsetOutOfSyncThreshold: utils.ToPtr[int64](200), SysOffsetSamples: utils.ToPtr[int64](5)}, clock.OSClockFilterConfig{InSyncThreshold: 50, OutOfSyncThreshold: 200, Samples: 5}},
		{"invalid samples", &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 100, SysOffsetSamples: utils.ToPtr[int64](0)}, clock.OSClockFilterConfig{InSyncThreshold: 100, OutOfSyncThreshold: 100, Samples: defaultSysOffsetSamples}},
		{"negative samples", &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 100, SysOffsetSamples: utils.ToPtr[int64](-1)}, clock.OSClockFilterConfig{InSyncThreshold: 100, OutOfSyncThreshold: 100, Samples: defaultSysOffsetSamples}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			profile := &ptpv1.PtpProfile{PtpClockThreshold: tt.threshold}
			if tt.threshold == nil {
				// Resolve the normal default through the same helper as production.
				tt.want.InSyncThreshold = float64(getPTPThreshold(profile).MaxOffsetThreshold)
				tt.want.OutOfSyncThreshold = tt.want.InSyncThreshold
			}
			assert.Equal(t, tt.want, osClockFilterConfig(profile))
		})
	}
}

func TestProcessParsedMetrics_Phc2sysEmitsRawSamples(t *testing.T) {
	p := &ptpProcess{
		ExecProcess:       ExecProcess{name: phc2sysProcessName, eventCh: make(chan event.Event, 10)},
		ptpClockThreshold: &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 100},
	}
	for _, tt := range []struct {
		offset float64
		raw    parserconstants.ClockState
		want   event.PTPState
	}{
		{10, "", event.PTP_NOTSET},
		{20, "", event.PTP_NOTSET},
		{150, parserconstants.ClockStateLocked, event.PTP_LOCKED},
		{10, parserconstants.ClockStateHoldover, event.PTP_HOLDOVER},
	} {
		processParsedMetrics(p, &parser.Metrics{Iface: "CLOCK_REALTIME", Offset: tt.offset, ClockState: tt.raw})
		require.Len(t, p.eventCh, 1, "even an undetermined raw sample must be forwarded")
		ev := <-p.eventCh
		assert.Equal(t, event.PHC2SYS, ev.Source)
		assert.Equal(t, tt.want, ev.Data.(*event.OffsetData).State)
		assert.Equal(t, int64(tt.offset), ev.Data.(*event.OffsetData).Offset)
	}
}
