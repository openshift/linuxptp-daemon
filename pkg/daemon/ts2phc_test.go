package daemon

import (
	"fmt"
	"math"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type ts2phcStaticExtractor struct {
	metric   *parser.Metrics
	ptpEvent *parser.PTPEvent
}

func (e ts2phcStaticExtractor) ProcessName() string { return ts2phcProcessName }

func (e ts2phcStaticExtractor) Extract(string) (*parser.Metrics, *parser.PTPEvent, error) {
	return e.metric, e.ptpEvent, nil
}

func TestTS2PHCPollSuccessIsDrivenByEvent(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")

	// Even a regular offset metric must not consume poll state on its own.
	p.logParser = ts2phcStaticExtractor{metric: &parser.Metrics{
		From: constants.TS2PHC, Iface: "ens0", Source: constants.Master, ClockState: constants.ClockStateLocked,
	}}
	processWithParser(p, "metric only")
	assert.False(t, p.ts2phcPoll.currentPollCycle.tstamps[0].handled)
	drainEvents(p.eventCh)

	// The typed success event is sufficient without a metric, including PHC
	// path resolution. It only updates poll tracking, not offset metrics.
	p.logParser = ts2phcStaticExtractor{ptpEvent: &parser.PTPEvent{
		Iface: "/dev/ptp0", Ts2Phc: &parser.Ts2PhcEvent{Timestamp: "2000.200", PollSuccessful: true},
	}}
	processWithParser(p, "event only")
	assert.True(t, p.ts2phcPoll.currentPollCycle.tstamps[0].handled)
	assert.Empty(t, drainEvents(p.eventCh))
}

func newTS2PHCPollTestProcess() *ptpProcess {
	InitializeOffsetMaps()
	p := &ptpProcess{
		ExecProcess: ExecProcess{
			name: ts2phcProcessName, configName: "ts2phc.1.config",
			messageTag: "[ts2phc.1.config]", eventCh: make(chan event.Event, 32),
		},
		ifaces: config.IFaces{
			{Name: "ens0", PhcId: "/dev/ptp0", Source: event.PTP4l},
			{Name: "ens1", PhcId: "/dev/ptp1", Source: event.PTP4l},
			{Name: "ens2", PhcId: "/dev/ptp2", Source: event.PTP4l},
		},
		logParser:         parser.NewTS2PHCExtractor(),
		ptpClockThreshold: &ptpv1.PtpClockThreshold{MaxOffsetThreshold: 10000},
	}
	p.resetPollCycle()
	return p
}

func feedTS2PHCPollLine(p *ptpProcess, timestamp, message string) {
	processWithParser(p, fmt.Sprintf("ts2phc[%s]: [ts2phc.1.config:7] %s", timestamp, message))
}

func TestPollGroupFromTimestamp(t *testing.T) {
	for _, tt := range []struct {
		timestamp string
		want      int
		wantErr   bool
	}{
		{timestamp: "55339.867", want: 55339},
		{timestamp: "55339", want: 55339},
		{timestamp: "invalid.867", wantErr: true},
		{timestamp: "", wantErr: true},
	} {
		t.Run(tt.timestamp, func(t *testing.T) {
			got, err := pollGroupFromTimestamp(tt.timestamp)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTS2PHCSelectionEvents(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	s := &p.ts2phcPoll
	assert.Equal(t, map[string]parser.TS2PHCRole{
		"/dev/ptp0": parser.TS2PHCSink,
		"/dev/ptp1": parser.TS2PHCSink,
		"/dev/ptp2": parser.TS2PHCSink,
	}, s.roles)
	assert.NotContains(t, s.roles, "/dev/ptp99", "unknown PHCs are not sinks")

	// Selection logs are events, not timestamps or offset metrics. They must
	// work even before the first poll and must not create a poll cycle.
	for _, action := range []string{"unselecting", "unselecting", "selecting", "selecting"} {
		feedTS2PHCPollLine(p, "2000.100", action+" /dev/ptp0 for synchronization")
		wantRole := parser.TS2PHCUnmanaged
		if action == "selecting" {
			wantRole = parser.TS2PHCSink
		}
		assert.Equal(t, wantRole, s.roles["/dev/ptp0"])
		assert.Equal(t, parser.TS2PHCSink, s.roles["/dev/ptp1"], "selection is per PHC")
		assert.Nil(t, s.currentPollCycle)
		assert.Zero(t, s.pollTimeoutCount)
		assert.Empty(t, drainEvents(p.eventCh))
	}

	other := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "2000.200", "unselecting /dev/ptp0 for synchronization")
	assert.Equal(t, parser.TS2PHCSink, other.ts2phcPoll.roles["/dev/ptp0"], "selection is per process")

	// Selection state must not survive a process restart.
	p.resetPollCycle()
	require.Len(t, s.roles, 3)
	for _, role := range s.roles {
		assert.Equal(t, parser.TS2PHCSink, role)
	}
	p.handlePHCSelection("", parser.TS2PHCUnmanaged)
	p.handlePHCSelection("/dev/ptp99", parser.TS2PHCSource)
	p.handlePHCSelection("/dev/ptp0", parser.TS2PHCRole("invalid"))
	assert.Len(t, s.roles, 3, "ignore unknown PHCs and invalid role changes")
	assert.Equal(t, parser.TS2PHCSink, s.roles["/dev/ptp0"])
}

func TestTS2PHCConfiguredRoles(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	p.ifaces = config.IFaces{
		{Name: "ens0", PhcId: "/dev/ptp0", Source: event.GNSS},
		{Name: "ens1", PhcId: "/dev/ptp0", Source: event.PTP4l},
		{Name: "ens2", PhcId: "/dev/ptp2"},
		{Name: "unresolved"},
	}
	p.resetPollCycle()
	assert.Equal(t, map[string]parser.TS2PHCRole{
		"/dev/ptp0": parser.TS2PHCSink,
		"/dev/ptp2": parser.TS2PHCSink,
	}, p.ts2phcPoll.roles, "initialize once per resolved PHC regardless of interface source labels")

	p.handlePHCSelection("/dev/ptp0", parser.TS2PHCSource)
	metrics := (&pollCycle{}).missingMetrics(p.ifaces, p.ts2phcPoll.roles)
	require.Len(t, metrics, 1, "all interface aliases of a non-sink PHC are excluded")
	assert.Equal(t, "ens2", metrics[0].Iface)

	p.ifaces = p.ifaces[2:]
	p.resetPollCycle()
	assert.Equal(t, map[string]parser.TS2PHCRole{
		"/dev/ptp2": parser.TS2PHCSink,
	}, p.ts2phcPoll.roles, "reset rebuilds from current configuration, removing stale PHCs")
}

func TestTS2PHCSharedPHCIsHandledByDevice(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	p.ifaces = config.IFaces{
		{Name: "ens0", PhcId: "/dev/ptp0"},
		{Name: "ens0alias", PhcId: "/dev/ptp0"},
	}
	p.resetPollCycle()
	_, err := p.handleAddedTstamp(&parser.Ts2PhcEvent{
		Timestamp: "2000.100", PHCDevice: "/dev/ptp0", PHCTstamp: &parser.PHCTimestamp{},
	})
	require.NoError(t, err)

	assert.Empty(t, p.ts2phcPoll.currentPollCycle.missingMetrics(p.ifaces, p.ts2phcPoll.roles),
		"one PHC timestamp covers every configured interface alias")
	p.handleSuccessfulPollFor("ens0alias")
	assert.True(t, p.ts2phcPoll.currentPollCycle.tstamps[0].handled,
		"successful offset for an alias must handle the shared PHC sample")
}

func TestTS2PHCRoleTransitions(t *testing.T) {
	roles := []parser.TS2PHCRole{parser.TS2PHCSink, parser.TS2PHCSource, parser.TS2PHCUnmanaged}
	for _, from := range roles {
		for _, to := range roles {
			t.Run(string(from)+" to "+string(to), func(t *testing.T) {
				p := newTS2PHCPollTestProcess()
				p.handlePHCSelection("/dev/ptp0", from)
				feedTS2PHCPollLine(p, "2000.100", "adding tstamp 10.0 to clock /dev/ptp0")
				p.handlePHCSelection("/dev/ptp0", to)
				s := &p.ts2phcPoll
				assert.Equal(t, to, s.roles["/dev/ptp0"])
				assert.Equal(t, parser.TS2PHCSink, s.roles["/dev/ptp1"])
				ts := &s.currentPollCycle.tstamps[0]
				assert.Equal(t, to, ts.role)
				assert.Equal(t, from != parser.TS2PHCSink || to != parser.TS2PHCSink, ts.handled)
				sources := s.currentPollCycle.sourceTimestamps()
				if to == parser.TS2PHCSource {
					require.Len(t, sources, 1)
					assert.Equal(t, "/dev/ptp0", sources[0].device)
				} else {
					assert.Empty(t, sources, "demotion must clear the old reference role")
				}

				// The next cycle uses the new role for newly received samples.
				_, err := p.handleAddedTstamp(&parser.Ts2PhcEvent{
					Timestamp: "2001.100", PHCDevice: "/dev/ptp0", PHCTstamp: &parser.PHCTimestamp{},
				})
				require.NoError(t, err)
				ts = &s.currentPollCycle.tstamps[0]
				assert.Equal(t, to != parser.TS2PHCSink, ts.handled)
				assert.Equal(t, to, ts.role)
			})
		}
	}
}

func TestTS2PHCSourceOffsets(t *testing.T) {
	for _, referenceAfterTimestamp := range []bool{false, true} {
		t.Run(fmt.Sprintf("referenceAfterTimestamp=%t", referenceAfterTimestamp), func(t *testing.T) {
			p := newTS2PHCPollTestProcess()
			if !referenceAfterTimestamp {
				feedTS2PHCPollLine(p, "2000.050", "selecting /dev/ptp0 as the reference clock")
			}
			feedTS2PHCPollLine(p, "2000.060", "unselecting /dev/ptp2 for synchronization")
			feedTS2PHCPollLine(p, "2000.100", "adding tstamp 10.0 to clock /dev/ptp0")
			feedTS2PHCPollLine(p, "2000.200", "adding tstamp 11.0 to clock /dev/ptp1")
			feedTS2PHCPollLine(p, "2000.300", "adding tstamp 100.0 to clock /dev/ptp2")
			if referenceAfterTimestamp {
				feedTS2PHCPollLine(p, "2000.350", "selecting /dev/ptp0 as the reference clock")
			}
			cycle := p.ts2phcPoll.currentPollCycle
			sources := cycle.sourceTimestamps()
			require.Len(t, sources, 1)
			assert.Equal(t, "/dev/ptp0", sources[0].device)
			metrics, err := p.handlePollTimeout(&parser.Ts2PhcEvent{Timestamp: "2000.400"})
			require.NoError(t, err)
			require.Len(t, metrics, 1, "only the sink emits a failure metric")
			assert.Equal(t, "ens1", metrics[0].Iface)
			assert.Equal(t, float64(-1_000_000_000), metrics[0].Offset,
				"source-minus-sink offset wins over larger absolute peer differences")
			assert.Equal(t, metrics[0].Offset, metrics[0].MaxOffset)

			// Explicit unselection removes reference status but retains the
			// timestamp for the absolute peer fallback.
			feedTS2PHCPollLine(p, "2000.500", "unselecting /dev/ptp0 for synchronization")
			assert.Empty(t, cycle.sourceTimestamps())
			assert.Equal(t, float64(89_000_000_000), computeTSOffset(&cycle.tstamps[1], cycle))
		})
	}
}

func TestTS2PHCReferenceSelectionEvents(t *testing.T) {
	for _, message := range []string{
		"unselecting /dev/ptp0 for synchronization",
		"selecting /dev/ptp0 as the reference clock",
		"no reference clock, selecting /dev/ptp0 by default",
	} {
		t.Run(message, func(t *testing.T) {
			p := newTS2PHCPollTestProcess()
			feedTS2PHCPollLine(p, "2000.050", "selecting /dev/ptp0 for synchronization")
			feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.25 to clock /dev/ptp0")
			feedTS2PHCPollLine(p, "2000.200", "adding tstamp 1791217457.0 to clock /dev/ptp1")
			feedTS2PHCPollLine(p, "2000.300", message)
			s := &p.ts2phcPoll
			wantRole := parser.TS2PHCSource
			if message == "unselecting /dev/ptp0 for synchronization" {
				wantRole = parser.TS2PHCUnmanaged
			}
			assert.Equal(t, wantRole, s.roles["/dev/ptp0"])
			assert.NotEqual(t, parser.TS2PHCSink, s.roles["/dev/ptp0"])
			assert.Equal(t, parser.TS2PHCSink, s.roles["/dev/ptp1"])
			assert.Empty(t, drainEvents(p.eventCh), "role changes emit no offset metrics")

			metrics, err := p.handlePollTimeout(&parser.Ts2PhcEvent{Timestamp: "2000.400"})
			require.NoError(t, err)
			require.Len(t, metrics, 1)
			assert.Equal(t, "ens1", metrics[0].Iface)
			assert.Equal(t, float64(250_000_000), metrics[0].Offset,
				"non-target timestamp remains available for peer comparisons")

			// Neither the explicit nor default reference should generate a
			// missing-clock failure; the other configured missing PHC still does.
			metrics = s.currentPollCycle.missingMetrics(p.ifaces, s.roles)
			require.Len(t, metrics, 1)
			assert.Equal(t, "ens2", metrics[0].Iface)

			feedTS2PHCPollLine(p, "2001.050", "selecting /dev/ptp0 for synchronization")
			assert.Equal(t, parser.TS2PHCSink, s.roles["/dev/ptp0"])
			_, err = p.handleAddedTstamp(&parser.Ts2PhcEvent{
				Timestamp: "2001.100", PHCDevice: "/dev/ptp0", PHCTstamp: &parser.PHCTimestamp{},
			})
			require.NoError(t, err)
			metrics, err = p.handlePollTimeout(&parser.Ts2PhcEvent{Timestamp: "2001.400"})
			require.NoError(t, err)
			require.Len(t, metrics, 1)
			assert.Equal(t, "ens0", metrics[0].Iface, "target reselection restores monitoring")
		})
	}
}

func TestTS2PHCUnselectedTimestampRemainsAvailableForOffsets(t *testing.T) {
	for _, unselectAfterTimestamp := range []bool{false, true} {
		t.Run(fmt.Sprintf("unselectAfterTimestamp=%t", unselectAfterTimestamp), func(t *testing.T) {
			p := newTS2PHCPollTestProcess()
			if !unselectAfterTimestamp {
				feedTS2PHCPollLine(p, "2000.050", "unselecting /dev/ptp0 for synchronization")
			}
			feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.25 to clock /dev/ptp0")
			feedTS2PHCPollLine(p, "2000.200", "adding tstamp 1791217457.0 to clock /dev/ptp1")
			if unselectAfterTimestamp {
				feedTS2PHCPollLine(p, "2000.300", "unselecting /dev/ptp0 for synchronization")
			}
			cycle := p.ts2phcPoll.currentPollCycle
			require.NotNil(t, cycle)
			require.Len(t, cycle.tstamps, 2)
			assert.True(t, cycle.tstamps[0].handled)
			metrics, err := p.handlePollTimeout(&parser.Ts2PhcEvent{Timestamp: "2000.400"})
			require.NoError(t, err)
			require.Len(t, metrics, 1)
			assert.Equal(t, "ens1", metrics[0].Iface)
			assert.Equal(t, float64(250_000_000), metrics[0].Offset)
			assert.Equal(t, metrics[0].Offset, metrics[0].MaxOffset)
		})
	}
}

func TestTS2PHCSelectionFiltersMissingPHCs(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "2000.050", "unselecting /dev/ptp0 for synchronization")
	feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp1")
	feedTS2PHCPollLine(p, "2000.200", "ens1 offset 0 s2 freq +0")
	drainEvents(p.eventCh)
	// No timestamp arrived for either ens0 or ens2. Only the still-monitored
	// ens2 should be reported missing when the next cycle begins.
	metrics, err := p.handleAddedTstamp(&parser.Ts2PhcEvent{
		Timestamp: "2001.100", PHCDevice: "/dev/ptp1", PHCTstamp: &parser.PHCTimestamp{},
	})
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	assert.Equal(t, "ens2", metrics[0].Iface)
	assert.True(t, math.IsNaN(metrics[0].Offset))

	// Reselecting the missing PHC restores missing-clock detection.
	feedTS2PHCPollLine(p, "2001.200", "selecting /dev/ptp0 for synchronization")
	metrics = p.ts2phcPoll.currentPollCycle.missingMetrics(p.ifaces, p.ts2phcPoll.roles)
	require.Len(t, metrics, 2)
	assert.Equal(t, "ens0", metrics[0].Iface)
	assert.Equal(t, "ens2", metrics[1].Iface)
}

func TestTS2PHCReselectionRestoresMonitoring(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "2000.050", "unselecting /dev/ptp0 for synchronization")
	feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "2000.200", "selecting /dev/ptp0 for synchronization")
	assert.True(t, p.ts2phcPoll.currentPollCycle.tstamps[0].handled,
		"reselection does not retroactively report an old unmonitored sample")
	metrics, err := p.handlePollTimeout(&parser.Ts2PhcEvent{Timestamp: "2000.400"})
	require.NoError(t, err)
	assert.Empty(t, metrics, "a timeout does not close a partially collected batch")
	_, err = p.handleAddedTstamp(&parser.Ts2PhcEvent{
		Timestamp: "2001.100", PHCDevice: "/dev/ptp0", PHCTstamp: &parser.PHCTimestamp{},
	})
	require.NoError(t, err)
	metrics, err = p.handlePollTimeout(&parser.Ts2PhcEvent{Timestamp: "2001.400"})
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	assert.Equal(t, "ens0", metrics[0].Iface)
}

func TestTS2PHCSelectionDoesNotManageUnknownPHCs(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "2000.050", "selecting /dev/ptp99 for synchronization")
	feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp99")
	metrics, err := p.handlePollTimeout(&parser.Ts2PhcEvent{Timestamp: "2000.400"})
	require.NoError(t, err)
	assert.Empty(t, metrics, "a timeout need not be the last event in a batch")
	assert.NotContains(t, p.ts2phcPoll.roles, "/dev/ptp99")
	feedTS2PHCPollLine(p, "2000.500", "selecting /dev/ptp99 as the reference clock")
	assert.NotContains(t, p.ts2phcPoll.roles, "/dev/ptp99", "observing a timestamp must not enroll an unknown PHC")
	assert.Equal(t, parser.TS2PHCUnmanaged, p.ts2phcPoll.currentPollCycle.tstamps[0].role)
}

func testTstamp(device string, timestamp float64, role parser.TS2PHCRole) tstamp {
	seconds := int64(math.Floor(timestamp))
	nanoseconds := int64(math.Round((timestamp - float64(seconds)) * 1e9))
	if nanoseconds == 1_000_000_000 {
		seconds++
		nanoseconds = 0
	}
	return tstamp{device: device, seconds: seconds, nanoseconds: nanoseconds, role: role}
}

func TestComputeTSOffset(t *testing.T) {
	stamp := func(device string, timestamp float64) *tstamp {
		stamp := testTstamp(device, timestamp, "")
		return &stamp
	}
	peers := func(timestamps ...tstamp) *pollCycle {
		return &pollCycle{tstamps: timestamps}
	}
	source := func(device string, timestamp float64) *pollCycle {
		return peers(testTstamp(device, timestamp, parser.TS2PHCSource))
	}
	tests := []struct {
		name  string
		ts    *tstamp
		cycle *pollCycle
		want  float64
	}{
		{
			name: "nil timestamp and cycle",
			want: math.NaN(),
		},
		{
			name: "nil cycle",
			ts:   stamp("/dev/ptp0", 10),
			want: math.NaN(),
		},
		{
			name:  "nil timestamp with empty cycle",
			cycle: &pollCycle{},
			want:  math.NaN(),
		},
		{
			name:  "nil timestamp with source",
			cycle: source("/dev/ptp1", 11),
			want:  math.NaN(),
		},
		{
			name:  "no source or peers",
			ts:    stamp("/dev/ptp0", 10),
			cycle: &pollCycle{},
			want:  math.NaN(),
		},
		{
			name:  "only self is present",
			ts:    stamp("/dev/ptp0", 10),
			cycle: peers(tstamp{device: "/dev/ptp0", seconds: 10, nanoseconds: 0}),
			want:  math.NaN(),
		},
		{
			name: "same device entries are excluded even with different timestamps",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp0", seconds: 9, nanoseconds: 0},
				tstamp{device: "/dev/ptp0", seconds: 11, nanoseconds: 0},
			),
			want: math.NaN(),
		},
		{
			name:  "explicit source ahead",
			ts:    stamp("/dev/ptp0", 10),
			cycle: source("/dev/ptp1", 11),
			want:  1_000_000_000,
		},
		{
			name:  "explicit source behind preserves sign",
			ts:    stamp("/dev/ptp0", 11),
			cycle: source("/dev/ptp1", 10),
			want:  -1_000_000_000,
		},
		{
			name:  "explicit source equal",
			ts:    stamp("/dev/ptp0", 10),
			cycle: source("/dev/ptp1", 10),
			want:  0,
		},
		{
			name:  "explicit source fractional difference across second boundary",
			ts:    stamp("/dev/ptp0", 10.75),
			cycle: source("/dev/ptp1", 11.25),
			want:  500_000_000,
		},
		{
			name:  "explicit source at realistic epoch",
			ts:    stamp("/dev/ptp0", 1791217457.25),
			cycle: source("/dev/ptp1", 1791217457.5),
			want:  250_000_000,
		},
		{
			name:  "zero timestamp is a valid measurement",
			ts:    stamp("/dev/ptp0", 0),
			cycle: source("/dev/ptp1", 0),
			want:  0,
		},
		{
			name:  "same-device source cannot measure an offset",
			ts:    stamp("/dev/ptp0", 10),
			cycle: source("/dev/ptp0", 11),
			want:  math.NaN(),
		},
		{
			name: "explicit source takes precedence over peers",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 9, nanoseconds: 0, role: parser.TS2PHCSource},
				tstamp{device: "/dev/ptp2", seconds: 20, nanoseconds: 0, role: parser.TS2PHCSink},
			),
			want: -1_000_000_000,
		},
		{
			name: "furthest source preserves its negative sign",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 11, nanoseconds: 0, role: parser.TS2PHCSource},
				tstamp{device: "/dev/ptp2", seconds: 7, nanoseconds: 0, role: parser.TS2PHCSource},
				tstamp{device: "/dev/ptp3", seconds: 100, nanoseconds: 0, role: parser.TS2PHCUnmanaged},
			),
			want: -3_000_000_000,
		},
		{
			name: "furthest source preserves its positive sign",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 9, nanoseconds: 0, role: parser.TS2PHCSource},
				tstamp{device: "/dev/ptp2", seconds: 13, nanoseconds: 0, role: parser.TS2PHCSource},
			),
			want: 3_000_000_000,
		},
		{
			name: "valid zero source offset does not fall back to peers",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 10, nanoseconds: 0, role: parser.TS2PHCSource},
				tstamp{device: "/dev/ptp2", seconds: 20, nanoseconds: 0, role: parser.TS2PHCSink},
			),
			want: 0,
		},
		{
			name: "same-device source falls back to another peer",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp0", seconds: 100, nanoseconds: 0, role: parser.TS2PHCSource},
				tstamp{device: "/dev/ptp1", seconds: 9, nanoseconds: 0, role: parser.TS2PHCSink},
			),
			want: 1_000_000_000,
		},
		// The peer fallback returns the largest absolute delta in nanoseconds.
		{
			name:  "peer ahead returns nanoseconds",
			ts:    stamp("/dev/ptp0", 10),
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 11, nanoseconds: 0}),
			want:  1_000_000_000,
		},
		{
			name:  "peer behind returns absolute delta",
			ts:    stamp("/dev/ptp0", 10),
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 9, nanoseconds: 0}),
			want:  1_000_000_000,
		},
		{
			name:  "equal peer is measured zero rather than unavailable",
			ts:    stamp("/dev/ptp0", 10),
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 10, nanoseconds: 0}),
			want:  0,
		},
		{
			name:  "fractional peer difference across second boundary",
			ts:    stamp("/dev/ptp0", 10.75),
			cycle: peers(tstamp{device: "/dev/ptp1", seconds: 11, nanoseconds: 250000000}),
			want:  500_000_000,
		},
		{
			name: "largest positive peer delta wins regardless of order",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 11, nanoseconds: 0},
				tstamp{device: "/dev/ptp2", seconds: 13, nanoseconds: 0},
				tstamp{device: "/dev/ptp3", seconds: 12, nanoseconds: 0},
			),
			want: 3_000_000_000,
		},
		{
			name: "negative delta with larger magnitude wins",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 1, nanoseconds: 0},
				tstamp{device: "/dev/ptp2", seconds: 11, nanoseconds: 0},
			),
			want: 9_000_000_000,
		},
		{
			name: "all peers behind return largest absolute delta",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp1", seconds: 8, nanoseconds: 0},
				tstamp{device: "/dev/ptp2", seconds: 9, nanoseconds: 0},
			),
			want: 2_000_000_000,
		},
		{
			name: "self cannot dominate peer comparison",
			ts:   stamp("/dev/ptp0", 10),
			cycle: peers(
				tstamp{device: "/dev/ptp0", seconds: 100, nanoseconds: 0},
				tstamp{device: "/dev/ptp1", seconds: 11, nanoseconds: 0},
			),
			want: 1_000_000_000,
		},
		{
			name: "peer eligibility depends on device not interface or handled status",
			ts:   &tstamp{device: "/dev/ptp0", iface: "ens0", seconds: 10, nanoseconds: 0, handled: true},
			cycle: peers(
				tstamp{device: "/dev/ptp1", iface: "ens0", seconds: 11, nanoseconds: 0, handled: true},
			),
			want: 1_000_000_000,
		},
		{
			name: "10ns offset",
			ts:   &tstamp{device: "/dev/ptp0", iface: "ens0", seconds: 1792458374, nanoseconds: 999999999, handled: true},
			cycle: peers(
				tstamp{device: "/dev/ptp1", iface: "ens0", seconds: 1792458374, nanoseconds: 999999989, handled: true},
			),
			want: 10,
		},
		{
			name: "1ns offset",
			ts:   &tstamp{device: "/dev/ptp0", iface: "ens0", seconds: 1792458374, nanoseconds: 999999999, handled: true},
			cycle: peers(
				tstamp{device: "/dev/ptp1", iface: "ens0", seconds: 1792458374, nanoseconds: 999999998, handled: true},
			),
			want: 1,
		},
		{
			name: "negative 10ns source offset",
			ts:   &tstamp{device: "/dev/ptp0", seconds: 1792458374, nanoseconds: 999999999},
			cycle: peers(tstamp{
				device: "/dev/ptp1", seconds: 1792458374, nanoseconds: 999999989, role: parser.TS2PHCSource,
			}),
			want: -10,
		},
		{
			name: "negative 1ns source offset",
			ts:   &tstamp{device: "/dev/ptp0", seconds: 1792458374, nanoseconds: 999999999},
			cycle: peers(tstamp{
				device: "/dev/ptp1", seconds: 1792458374, nanoseconds: 999999998, role: parser.TS2PHCSource,
			}),
			want: -1,
		},
		{
			name: "positive 100-year source offset",
			ts:   &tstamp{device: "/dev/ptp0", seconds: 1_000_000_000},
			cycle: peers(tstamp{
				device: "/dev/ptp1", seconds: 4_155_760_000, role: parser.TS2PHCSource,
			}),
			want: 3_155_760_000_000_000_000,
		},
		{
			name: "negative 100-year source offset",
			ts:   &tstamp{device: "/dev/ptp0", seconds: 4_155_760_000},
			cycle: peers(tstamp{
				device: "/dev/ptp1", seconds: 1_000_000_000, role: parser.TS2PHCSource,
			}),
			want: -3_155_760_000_000_000_000,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeTSOffset(tt.ts, tt.cycle)
			if math.IsNaN(tt.want) {
				assert.True(t, math.IsNaN(got), "unmeasured offset must be NaN, got %v", got)
				return
			}
			// Integer seconds and nanoseconds keep these assertions independent
			// of floating-point rounding at epoch-sized timestamps.
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTS2PHCPollCycleSuccessfulPoll(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	for i := range 3 {
		feedTS2PHCPollLine(p, "1063.353", fmt.Sprintf("adding tstamp 1791217457.000000000 to clock /dev/ptp%d", i))
	}
	require.NotNil(t, p.ts2phcPoll.currentPollCycle)
	assert.Equal(t, 1063, p.ts2phcPoll.currentPollCycle.group)
	require.Len(t, p.ts2phcPoll.currentPollCycle.tstamps, 3)
	assert.Empty(t, drainEvents(p.eventCh))

	// Both interface names and raw PHC paths must consume their own timestamp,
	// rather than discarding the cycle on the first successful offset.
	for i, iface := range []string{"ens0", "/dev/ptp1", "ens2"} {
		feedTS2PHCPollLine(p, "1063.580", iface+" offset 0 s2 freq +0")
		assert.True(t, p.ts2phcPoll.currentPollCycle.tstamps[i].handled)
	}
	assert.Len(t, drainEvents(p.eventCh), 3)
	feedTS2PHCPollLine(p, "1063.913", "poll returns zero, no events")
	assert.Empty(t, drainEvents(p.eventCh), "successful timestamps must not be reported as failures")
	feedTS2PHCPollLine(p, "1064.100", "adding tstamp 1791217458.0 to clock /dev/ptp0")
	assert.Empty(t, drainEvents(p.eventCh), "rollover must not report already consumed timestamps")
}

func TestTS2PHCPollCycleTimeoutSequence(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "1066.140", "adding tstamp 1791217460.000000097 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "1066.353", "adding tstamp 1791217460.000084400 to clock /dev/ptp1")
	feedTS2PHCPollLine(p, "1066.473", "poll returns zero, no events")
	events := drainEvents(p.eventCh)
	require.Len(t, events, 2)
	for i, ev := range events {
		assert.Equal(t, fmt.Sprintf("ens%d", i), ev.IFace)
		assert.Equal(t, event.TS2PHC, ev.Source)
		assert.Equal(t, "ts2phc.1.config", ev.CfgName)
		require.IsType(t, &event.OffsetData{}, ev.Data)
		assert.Equal(t, event.PTP_FREERUN, ev.Data.(*event.OffsetData).State)
		assert.True(t, p.ts2phcPoll.currentPollCycle.tstamps[i].handled)
	}
	assert.True(t, p.TS2phcHasTimeouts())
	assert.Equal(t, 1, p.ts2phcPoll.pollTimeoutCount)

	// A later timestamp in the same group is reported by the next timeout,
	// but the group is still counted only once.
	feedTS2PHCPollLine(p, "1066.580", "adding tstamp 1791217460.000000000 to clock /dev/ptp2")
	feedTS2PHCPollLine(p, "1066.913", "poll returns zero, no events")
	events = drainEvents(p.eventCh)
	require.Len(t, events, 1)
	assert.Equal(t, "ens2", events[0].IFace)
	assert.Equal(t, 1, p.ts2phcPoll.pollTimeoutCount)
	feedTS2PHCPollLine(p, "1066.950", "poll returns zero, no events")
	assert.Empty(t, drainEvents(p.eventCh))
}

func TestTS2PHCBatchSpansLogSecond(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	p.ifaces = p.ifaces[:2]
	p.resetPollCycle()
	feedTS2PHCPollLine(p, "900.999", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "901.001", "adding tstamp 1791217457.1 to clock /dev/ptp1")
	assert.Empty(t, drainEvents(p.eventCh), "log-second boundary must not emit premature FREERUN")
	require.Len(t, p.ts2phcPoll.currentPollCycle.tstamps, 2)
	feedTS2PHCPollLine(p, "901.100", "ens0 offset 0 s2 freq +0")
	feedTS2PHCPollLine(p, "901.200", "ens1 offset 0 s2 freq +0")
	assert.Len(t, drainEvents(p.eventCh), 2)
	feedTS2PHCPollLine(p, "901.999", "adding tstamp 1791217458.0 to clock /dev/ptp0")
	assert.Empty(t, drainEvents(p.eventCh), "the completed batch should not produce failures")
}

func TestTS2PHCTimeoutAndLateTimestampAcrossLogSecond(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "900.800", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "900.900", "poll returns zero, no events")
	assert.Len(t, drainEvents(p.eventCh), 1)
	feedTS2PHCPollLine(p, "901.001", "adding tstamp 1791217457.1 to clock /dev/ptp1")
	feedTS2PHCPollLine(p, "901.100", "poll returns zero, no events")
	events := drainEvents(p.eventCh)
	require.Len(t, events, 1)
	assert.Equal(t, "ens1", events[0].IFace)
	require.Len(t, p.ts2phcPoll.currentPollCycle.tstamps, 2)
}

func TestTS2PHCTimeoutThenLateTimestampSameBatch(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "900.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "900.200", "adding tstamp 1791217457.1 to clock /dev/ptp1")
	feedTS2PHCPollLine(p, "900.400", "poll returns zero, no events")
	first := drainEvents(p.eventCh)
	require.Len(t, first, 2)
	feedTS2PHCPollLine(p, "900.500", "adding tstamp 1791217457.2 to clock /dev/ptp2")
	feedTS2PHCPollLine(p, "900.700", "poll returns zero, no events")
	second := drainEvents(p.eventCh)
	require.Len(t, second, 1)
	assert.Equal(t, "ens2", second[0].IFace)
	assert.Equal(t, 1, p.ts2phcPoll.pollTimeoutCount)
	feedTS2PHCPollLine(p, "900.800", "poll returns zero, no events")
	assert.Empty(t, drainEvents(p.eventCh))
}

func TestTS2PHCPollCycleRollover(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "2000.200", "adding tstamp 1791217457.0 to clock /dev/ptp1")
	feedTS2PHCPollLine(p, "2000.300", "ens0 offset 0 s2 freq +0")
	drainEvents(p.eventCh)
	feedTS2PHCPollLine(p, "2001.100", "adding tstamp 1791217458.0 to clock /dev/ptp2")
	assert.Empty(t, drainEvents(p.eventCh), "a new log second alone cannot close a batch")
	feedTS2PHCPollLine(p, "2001.200", "adding tstamp 1791217458.0 to clock /dev/ptp0")
	events := drainEvents(p.eventCh)
	require.Len(t, events, 2)
	assert.Equal(t, "ens1", events[0].IFace)
	assert.Equal(t, event.PTP_FREERUN, events[0].Data.(*event.OffsetData).State)
	assert.Equal(t, "ens2", events[1].IFace, "late PHC sample stays in the preceding batch")
	require.IsType(t, &event.OffsetData{}, events[1].Data)
	assert.Equal(t, event.PTP_FREERUN, events[1].Data.(*event.OffsetData).State)
	assert.Equal(t, 2001, p.ts2phcPoll.currentPollCycle.group)
	require.Len(t, p.ts2phcPoll.currentPollCycle.tstamps, 1)
	assert.False(t, p.ts2phcPoll.currentPollCycle.tstamps[0].handled)
}

func TestTS2PHCTimeoutRolloverReportsMissingSinks(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	p.handlePHCSelection("/dev/ptp0", parser.TS2PHCSource)
	_, err := p.handleAddedTstamp(&parser.Ts2PhcEvent{
		Timestamp: "2000.100", PHCDevice: "/dev/ptp0", PHCTstamp: &parser.PHCTimestamp{},
	})
	require.NoError(t, err)

	metrics, err := p.handlePollTimeout(&parser.Ts2PhcEvent{Timestamp: "2001.400"})
	require.NoError(t, err)
	assert.Empty(t, metrics, "a timeout across a log boundary may still precede more samples")
	metrics, err = p.handlePollTimeout(&parser.Ts2PhcEvent{Timestamp: "2002.400"})
	require.NoError(t, err)
	require.Len(t, metrics, 2, "a subsequent timeout group reports missing sinks")
	assert.Equal(t, []string{"ens1", "ens2"}, []string{metrics[0].Iface, metrics[1].Iface})
	assert.True(t, math.IsNaN(metrics[0].Offset))
	assert.True(t, math.IsNaN(metrics[1].Offset))
	cycle := p.ts2phcPoll.currentPollCycle
	require.NotNil(t, cycle)
	assert.Equal(t, 2002, cycle.group)
	assert.Empty(t, cycle.tstamps)
	assert.True(t, cycle.counted)
}

func TestTS2PHCPollTimeoutCountAndRecovery(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "2000.000", "poll returns zero, no events")
	assert.False(t, p.TS2phcHasTimeouts())
	for i := range 2 {
		feedTS2PHCPollLine(p, fmt.Sprintf("%d.100", 2000+i), "adding tstamp 1791217457.0 to clock /dev/ptp0")
		feedTS2PHCPollLine(p, fmt.Sprintf("%d.400", 2000+i), "poll returns zero, no events")
		feedTS2PHCPollLine(p, fmt.Sprintf("%d.800", 2000+i), "poll returns zero, no events")
		assert.Equal(t, i+1, p.ts2phcPoll.pollTimeoutCount)
	}
	feedTS2PHCPollLine(p, "2002.100", "adding tstamp 1791217459.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "2002.400", "ens0 offset 0 s2 freq +0")
	assert.False(t, p.TS2phcHasTimeouts())
	assert.True(t, p.ts2phcPoll.currentPollCycle.tstamps[0].handled)
	p.resetPollCycle()
	assert.Nil(t, p.ts2phcPoll.currentPollCycle)
	assert.False(t, p.TS2phcHasTimeouts())
}

func TestTS2PHCPollTimeoutAfterSecondsBoundary(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "2000.900", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "2001.100", "poll returns zero, no events")
	cycle := p.ts2phcPoll.currentPollCycle
	require.NotNil(t, cycle, "a timeout after polling starts retains the current group")
	assert.Equal(t, 2000, cycle.group, "the timestamp batch remains open across the log-second boundary")
	require.Len(t, cycle.tstamps, 1)
	assert.True(t, cycle.counted)
	events := drainEvents(p.eventCh)
	require.Len(t, events, 1, "only received timestamps are reported until the batch boundary is known")
	assert.Equal(t, "ens0", events[0].IFace)
	assert.Equal(t, 1, p.ts2phcPoll.pollTimeoutCount)
	feedTS2PHCPollLine(p, "2001.400", "poll returns zero, no events")
	assert.Empty(t, drainEvents(p.eventCh), "repeated timeout in the same group must not duplicate failures")
	assert.Equal(t, 1, p.ts2phcPoll.pollTimeoutCount)
}

func TestTS2PHCHealthyCycleFollowedByTimeoutOnlySeconds(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	for i := range 3 {
		feedTS2PHCPollLine(p, "2000.100", fmt.Sprintf("adding tstamp 1791217457.0 to clock /dev/ptp%d", i))
	}
	for _, iface := range []string{"ens0", "ens1", "ens2"} {
		feedTS2PHCPollLine(p, "2000.400", iface+" offset 0 s2 freq +0")
	}
	healthyEvents := drainEvents(p.eventCh)
	require.Len(t, healthyEvents, 3, "the healthy poll should emit one successful metric per sink")
	for _, ev := range healthyEvents {
		require.IsType(t, &event.OffsetData{}, ev.Data)
		assert.Equal(t, event.PTP_LOCKED, ev.Data.(*event.OffsetData).State)
	}

	for group, timestamp := range []string{"2001.100", "2002.100"} {
		feedTS2PHCPollLine(p, timestamp, "poll returns zero, no events")
		events := drainEvents(p.eventCh)
		require.Len(t, events, 3, "each timeout-only second reports every configured sink once")
		assert.Equal(t, []string{"ens0", "ens1", "ens2"}, []string{events[0].IFace, events[1].IFace, events[2].IFace})
		for _, ev := range events {
			require.IsType(t, &event.StateData{}, ev.Data)
			assert.Equal(t, event.PTP_FREERUN, ev.Data.(*event.StateData).State)
		}

		feedTS2PHCPollLine(p, timestamp[:4]+".700", "poll returns zero, no events")
		assert.Empty(t, drainEvents(p.eventCh), "multiple timeout events in one group must not duplicate sink reports")
		assert.Equal(t, group+1, p.ts2phcPoll.pollTimeoutCount)
	}
}

func TestTS2PHCPollStateIsPerProcess(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	other := newTS2PHCPollTestProcess()
	other.ifaces = config.IFaces{{Name: "other0", PhcId: "/dev/ptp0"}}
	feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(other, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "2000.400", "poll returns zero, no events")
	assert.False(t, other.TS2phcHasTimeouts())
	assert.False(t, other.ts2phcPoll.currentPollCycle.tstamps[0].handled)
	feedTS2PHCPollLine(other, "2000.400", "poll returns zero, no events")
	events := drainEvents(other.eventCh)
	require.Len(t, events, 1)
	assert.Equal(t, "other0", events[0].IFace)
}

func TestTS2PHCPollIgnoresUnmanagedPHCs(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	// Configure only the clock this test supplies; other configured clocks
	// would legitimately produce missing-clock metrics at rollover.
	p.ifaces = p.ifaces[:1]
	feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp99")
	feedTS2PHCPollLine(p, "2000.200", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "2000.400", "poll returns zero, no events")
	events := drainEvents(p.eventCh)
	require.Len(t, events, 1)
	assert.Equal(t, "ens0", events[0].IFace)
	feedTS2PHCPollLine(p, "2001.100", "adding tstamp 1791217458.0 to clock /dev/ptp99")
	assert.Empty(t, drainEvents(p.eventCh))
}

func TestTS2PHCNMEAStatusDoesNotConsumePoll(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	feedTS2PHCPollLine(p, "2000.100", "adding tstamp 1791217457.0 to clock /dev/ptp0")
	feedTS2PHCPollLine(p, "2000.200", "ens0 nmea_status 1 offset 0 s2")
	assert.False(t, p.ts2phcPoll.currentPollCycle.tstamps[0].handled)
	drainEvents(p.eventCh)
	feedTS2PHCPollLine(p, "2000.400", "poll returns zero, no events")
	events := drainEvents(p.eventCh)
	require.Len(t, events, 1)
	assert.Equal(t, "ens0", events[0].IFace)
}

func TestTS2PHCPollRejectsInvalidEvents(t *testing.T) {
	p := newTS2PHCPollTestProcess()
	_, err := p.handleAddedTstamp(&parser.Ts2PhcEvent{Timestamp: "invalid"})
	require.Error(t, err)
	_, err = p.handleAddedTstamp(&parser.Ts2PhcEvent{Timestamp: "2000"})
	require.Error(t, err)
	_, err = p.handlePollTimeout(&parser.Ts2PhcEvent{Timestamp: "invalid"})
	require.Error(t, err)
	p.processTS2PhcEvent(&parser.PTPEvent{})
	assert.Nil(t, p.ts2phcPoll.currentPollCycle)
	assert.Empty(t, drainEvents(p.eventCh))
}

func TestTS2PHCPollFailureMetrics(t *testing.T) {
	cycle := &pollCycle{tstamps: []tstamp{
		{iface: "ens0", role: parser.TS2PHCSink},
		{iface: "ens1", role: parser.TS2PHCSink, handled: true},
		{iface: "ens2", role: parser.TS2PHCSink},
	}}
	metrics := cycle.unhandledMetrics()
	require.Len(t, metrics, 2)
	for i, iface := range []string{"ens0", "ens2"} {
		assert.True(t, math.IsNaN(metrics[i].Offset))
		assert.True(t, math.IsNaN(metrics[i].MaxOffset))
		// NaN is not equal to itself; check the other fields separately.
		got := *metrics[i]
		got.Offset, got.MaxOffset = 0, 0
		assert.Equal(t, parser.Metrics{
			From: constants.TS2PHC, Iface: iface, ClockState: constants.ClockStateFreeRun, Source: constants.Master,
		}, got)
	}
	assert.Empty(t, cycle.unhandledMetrics())
}
