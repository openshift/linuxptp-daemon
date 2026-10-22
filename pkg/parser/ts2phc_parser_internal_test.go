package parser

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTS2PHCParser(t *testing.T) {
	tests := []struct {
		name           string
		logLine        string
		regex          *regexp.Regexp
		expectedSkip   bool
		expectedResult *ts2phcParsed
	}{
		{
			name:    "Valid metrics for master",
			regex:   regularTS2PhcRegex,
			logLine: "ts2phc[82674.465]: [ts2phc.0.cfg] ens2f1 master offset        2345 s2 freq      -16642",
			expectedResult: &ts2phcParsed{
				Timestamp: "82674.465", ConfigName: "ts2phc.0.cfg", Interface: "ens2f1",
				ClockName: "master", Offset: _ptr(2345.0), ServoState: "s2", FreqAdj: _ptr(-16642.0),
			},
		},
		{
			name:    "Valid metrics for clock path",
			regex:   regularTS2PhcRegex,
			logLine: "ts2phc[82674.4656]: [ts2phc.0.cfg] /dev/ptp6 offset        3152778 s2 freq      -3152778",
			expectedResult: &ts2phcParsed{
				Timestamp: "82674.4656", ConfigName: "ts2phc.0.cfg", Interface: "/dev/ptp6",
				Offset: _ptr(3152778.0), ServoState: "s2", FreqAdj: _ptr(-3152778.0),
			},
		},
		{
			name:    "Valid metrics with master offset with holdover",
			regex:   regularTS2PhcRegex,
			logLine: "ts2phc[521734.693]: [ts2phc.0.config:6] ens2f2 offset      -1 s2 freq      -3972 holdover",
			expectedResult: &ts2phcParsed{
				Timestamp: "521734.693", ConfigName: "ts2phc.0.config", SeverityLevel: _ptr(6),
				Interface: "ens2f2", Offset: _ptr(-1.0), ServoState: "s2", FreqAdj: _ptr(-3972.0), Holdover: "holdover",
			},
		},
		{
			name: "Invalid log line", regex: regularTS2PhcRegex, logLine: "invalid log line", expectedSkip: true,
		},
		{
			name: "Empty log line", regex: regularTS2PhcRegex, logLine: "", expectedSkip: true,
		},
		{
			name:    "Valid NMEA status with value 1",
			regex:   nmeaStatusTS2PhcRegex,
			logLine: "ts2phc[1726600506]:[ts2phc.0.config] ens7f0 nmea_status 1 offset 0 s2",
			expectedResult: &ts2phcParsed{
				Timestamp: "1726600506", ConfigName: "ts2phc.0.config", Interface: "ens7f0",
				Status: _ptr(1.0), Offset: _ptr(0.0), ServoState: "s2",
			},
		},
		{
			name:    "Valid NMEA status with value 0",
			regex:   nmeaStatusTS2PhcRegex,
			logLine: "ts2phc[1699929121]:[ts2phc.0.config] ens2f0 nmea_status 0 offset 999999 s0",
			expectedResult: &ts2phcParsed{
				Timestamp: "1699929121", ConfigName: "ts2phc.0.config", Interface: "ens2f0",
				Status: _ptr(0.0), Offset: _ptr(999999.0), ServoState: "s0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewTS2PHCExtractor()
			res, parsed, err := parseLine(tt.logLine, tt.regex, extractor.NewParsed)
			require.NoError(t, err)
			if tt.expectedSkip {
				assert.False(t, parsed)
			} else {
				require.True(t, parsed)
				tt.expectedResult.Raw = tt.logLine
				assert.Equal(t, tt.expectedResult, res)
			}
		})
	}
}

func TestTS2PHCOffsetReturnsMetricAndEvent(t *testing.T) {
	for _, tt := range []struct {
		name  string
		iface string
		line  string
	}{
		{
			name: "interface name", iface: "ens2f1",
			line: "ts2phc[82674.465]: [ts2phc.0.cfg] ens2f1 master offset 2345 s2 freq -16642",
		},
		{
			name: "PHC device", iface: "/dev/ptp6",
			line: "ts2phc[82674.465]: [ts2phc.0.config:6] /dev/ptp6 offset 2345 s2 freq -16642",
		},
		{
			name: "holdover", iface: "ens2f1",
			line: "ts2phc[82674.465]: [ts2phc.0.config:6] ens2f1 offset 2345 s2 freq -16642 holdover",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			metric, event, err := NewTS2PHCExtractor().Extract(tt.line)
			require.NoError(t, err)
			require.NotNil(t, metric)
			assert.Equal(t, tt.iface, metric.Iface)
			assert.Equal(t, float64(2345), metric.Offset)
			assert.Equal(t, float64(-16642), metric.FreqAdj)
			require.NotNil(t, event)
			assert.Equal(t, tt.iface, event.Iface)
			assert.Equal(t, tt.line, event.Raw)
			assert.Equal(t, &Ts2PhcEvent{Timestamp: "82674.465", PollSuccessful: true}, event.Ts2Phc)
		})
	}
}

func TestTS2PHCInvalidOffsetDoesNotReturnSuccessEvent(t *testing.T) {
	for _, parsed := range []*ts2phcParsed{
		{Offset: _ptr(0.0), FreqAdj: _ptr(0.0)},
		{Interface: "ens0", FreqAdj: _ptr(0.0)},
		{Interface: "ens0", Offset: _ptr(0.0)},
	} {
		metric, event, err := extractTS2PHCOffset(parsed)
		require.Error(t, err)
		assert.Nil(t, metric)
		assert.Nil(t, event)
	}
}

func TestTS2PHCNMEAStatusDoesNotReturnSuccessEvent(t *testing.T) {
	metric, event, err := NewTS2PHCExtractor().Extract("ts2phc[82674.465]: [ts2phc.0.config:6] ens0 nmea_status 1 offset 0 s2")
	require.NoError(t, err)
	require.NotNil(t, metric)
	assert.Nil(t, event)
}

func TestTS2PHCReferenceEventExtraction(t *testing.T) {
	for _, tt := range []struct {
		name string
		line string
		want *Ts2PhcEvent
	}{
		{
			name: "explicit reference with severity",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] selecting /dev/ptp9 as the reference clock",
			want: &Ts2PhcEvent{Timestamp: "9979.140", PHCDevice: "/dev/ptp9", Role: TS2PHCSource},
		},
		{
			name: "explicit reference without severity or space after colon",
			line: "ts2phc[9979]:[ts2phc.1.cfg] selecting /dev/ptp0 as the reference clock",
			want: &Ts2PhcEvent{Timestamp: "9979", PHCDevice: "/dev/ptp0", Role: TS2PHCSource},
		},
		{
			name: "default reference with severity",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] no reference clock, selecting /dev/ptp9 by default",
			want: &Ts2PhcEvent{Timestamp: "9979.140", PHCDevice: "/dev/ptp9", Role: TS2PHCSource},
		},
		{
			name: "default reference without severity or space after colon",
			line: "ts2phc[9979]:[ts2phc.1.cfg] no reference clock, selecting /dev/ptp0 by default",
			want: &Ts2PhcEvent{Timestamp: "9979", PHCDevice: "/dev/ptp0", Role: TS2PHCSource},
		},
		{
			name: "explicit reference missing PHC",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] selecting as the reference clock",
		},
		{
			name: "default reference invalid PHC",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] no reference clock, selecting /dev/ptpX by default",
		},
		{
			name: "explicit reference trailing text",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] selecting /dev/ptp9 as the reference clock failed",
		},
		{
			name: "default reference trailing text",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] no reference clock, selecting /dev/ptp9 by default failed",
		},
		{
			name: "other process",
			line: "phc2sys[9979.140]: [ts2phc.1.config:6] selecting /dev/ptp9 as the reference clock",
		},
		{
			name: "external reference has no PHC target to update",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] assuming external reference clock",
		},
		{
			name: "waiting for reference has no PHC target to update",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] reference clock not ready, waiting...",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			extractor := NewTS2PHCExtractor()
			for range 2 {
				metric, event, err := extractor.Extract(tt.line)
				require.NoError(t, err)
				assert.Nil(t, metric)
				if tt.want == nil {
					assert.Nil(t, event)
					continue
				}
				require.NotNil(t, event)
				assert.Equal(t, tt.line, event.Raw)
				assert.Equal(t, tt.want, event.Ts2Phc)
			}
		})
	}
}

func TestTS2PHCPollEventExtraction(t *testing.T) {
	tests := []struct {
		name string
		line string
		want *Ts2PhcEvent
	}{
		{
			name: "timestamp with PHC device",
			line: "ts2phc[9979.140]: [ts2phc.1.config:7] adding tstamp 1791226372.999999988 to clock /dev/ptp0",
			want: &Ts2PhcEvent{Timestamp: "9979.140", PHCDevice: "/dev/ptp0", PHCTstamp: &PHCTimestamp{Seconds: 1791226372, Nanoseconds: 999999988}},
		},
		{
			name: "timestamp without severity",
			line: "ts2phc[9979]: [ts2phc.1.cfg] adding tstamp 1791226372.000000000 to clock /dev/ptp99",
			want: &Ts2PhcEvent{Timestamp: "9979", PHCDevice: "/dev/ptp99", PHCTstamp: &PHCTimestamp{Seconds: 1791226372}},
		},
		{
			name: "poll timeout",
			line: "ts2phc[9979.473]: [ts2phc.1.config:7] poll returns zero, no events",
			want: &Ts2PhcEvent{Timestamp: "9979.473", PollTimeout: true},
		},
		{
			name: "poll timeout without severity",
			line: "ts2phc[9979]: [ts2phc.1.cfg] poll returns zero, no events",
			want: &Ts2PhcEvent{Timestamp: "9979", PollTimeout: true},
		},
		{
			name: "selecting PHC",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] selecting /dev/ptp9 for synchronization",
			want: &Ts2PhcEvent{Timestamp: "9979.140", PHCDevice: "/dev/ptp9", Role: TS2PHCSink},
		},
		{
			name: "unselecting PHC without severity or spacing after colon",
			line: "ts2phc[9979]:[ts2phc.1.cfg] unselecting /dev/ptp9 for synchronization",
			want: &Ts2PhcEvent{Timestamp: "9979", PHCDevice: "/dev/ptp9", Role: TS2PHCUnmanaged},
		},
		{
			name: "selection missing PHC",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] selecting for synchronization",
		},
		{
			name: "selection with invalid PHC",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] selecting /dev/ptpX for synchronization",
		},
		{
			name: "selection with unrelated suffix",
			line: "ts2phc[9979.140]: [ts2phc.1.config:6] selecting /dev/ptp9 for synchronization failed",
		},
		{
			name: "selection from another process",
			line: "phc2sys[9979.140]: [ts2phc.1.config:6] selecting /dev/ptp9 for synchronization",
		},
		{
			name: "malformed hardware timestamp",
			line: "ts2phc[9979.140]: [ts2phc.1.config:7] adding tstamp invalid to clock /dev/ptp0",
		},
		{
			name: "missing PHC",
			line: "ts2phc[9979.140]: [ts2phc.1.config:7] adding tstamp 1791226372.0 to clock",
		},
		{
			name: "unrelated log line",
			line: "unrelated log line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Extraction needs no configured interfaces and holds no poll state.
			extractor := NewTS2PHCExtractor()
			for i := 0; i < 2; i++ {
				metric, event, err := extractor.Extract(tt.line)
				require.NoError(t, err)
				assert.Nil(t, metric)
				if tt.want == nil {
					assert.Nil(t, event)
					continue
				}
				require.NotNil(t, event)
				assert.Equal(t, tt.line, event.Raw)
				assert.Equal(t, tt.want, event.Ts2Phc)
			}
		})
	}
}
