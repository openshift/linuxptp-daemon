package daemon

import (
	"context"
	"os/exec"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Common ubxtool line fixtures reused across multiple test cases.
const (
	navStatusHeader = "UBX-NAV-STATUS:\n"
	navStatus3DFix  = "  iTOW 437000 gpsFix 3 flags 0xdd fixStat 0 flags2 0x08\n"
	navClockHeader  = "UBX-NAV-CLOCK:\n"
)

func TestResetSerialPort(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		serialPort string
		runner     func(ctx context.Context, name string, args ...string) *exec.Cmd
		wantErr    bool
	}{
		{
			name:       "happy path: stty succeeds",
			serialPort: "/dev/ttyUSB0",
			runner: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "true")
			},
			wantErr: false,
		},
		{
			name:       "stty command fails",
			serialPort: "/dev/ttyUSB0",
			runner: func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "false")
			},
			wantErr: true,
		},
		{
			name:       "empty serialPort skips reset without error",
			serialPort: "",
			runner: func(_ context.Context, _ string, _ ...string) *exec.Cmd {
				t.Error("cmdRunner must not be called when serialPort is empty")
				return nil
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := &GPSD{
				serialPort: tt.serialPort,
				cmdRunner:  tt.runner,
			}
			err := g.resetSerialPort(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestResetSerialPortPassesCorrectArgs(t *testing.T) {
	t.Parallel()

	const device = "/dev/ttyS0"
	var capturedName string
	var capturedArgs []string

	g := &GPSD{
		serialPort: device,
		cmdRunner: func(ctx context.Context, name string, args ...string) *exec.Cmd {
			capturedName = name
			capturedArgs = args
			return exec.CommandContext(ctx, "true")
		},
	}

	err := g.resetSerialPort(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, "stty", capturedName)
	assert.Equal(t, []string{"-F", device, "sane"}, capturedArgs)
}

func TestProcessGNSSLines(t *testing.T) {
	tests := []struct {
		name           string
		lines          []string
		maxOffset      int64
		minOffset      int64
		wantSourceLost bool
		wantOffset     int64
		wantGPSStatus  int64
		wantTimeLs     *ublox.TimeLs // non-nil: assert TimeLs dispatched to leap manager
	}{
		{
			name: "locked with good fix and offset in range",
			lines: []string{
				navStatusHeader,
				navStatus3DFix,
				navClockHeader,
				"  iTOW 437000 clkBias 42 clkDrift 0 tAcc 23 fAcc 0\n",
			},
			maxOffset:      100,
			minOffset:      -100,
			wantSourceLost: false,
			wantOffset:     23,
			wantGPSStatus:  3,
		},
		{
			name: "lines without trailing newline are also handled",
			lines: []string{
				"UBX-NAV-STATUS:",
				"  iTOW 437000 gpsFix 3 flags 0xdd fixStat 0 flags2 0x08",
				"UBX-NAV-CLOCK:",
				"  iTOW 437000 clkBias 42 clkDrift 0 tAcc 23 fAcc 0",
			},
			maxOffset:      100,
			minOffset:      -100,
			wantSourceLost: false,
			wantOffset:     23,
			wantGPSStatus:  3,
		},
		{
			name: "freerun with no fix",
			lines: []string{
				navStatusHeader,
				"  iTOW 437000 gpsFix 0 flags 0x00 fixStat 0 flags2 0x00\n",
				navClockHeader,
				"  iTOW 437000 clkBias 42 clkDrift 0 tAcc 50 fAcc 0\n",
			},
			maxOffset:      100,
			minOffset:      -100,
			wantSourceLost: true,
			wantOffset:     50,
			wantGPSStatus:  0,
		},
		{
			name: "freerun when offset out of range despite good fix",
			lines: []string{
				navStatusHeader,
				navStatus3DFix,
				navClockHeader,
				"  iTOW 437000 clkBias 42 clkDrift 0 tAcc 5000 fAcc 0\n",
			},
			maxOffset:      100,
			minOffset:      -100,
			wantSourceLost: true,
			wantOffset:     5000,
			wantGPSStatus:  3,
		},
		{
			name: "TIMELS dispatched alongside normal fix",
			lines: []string{
				navStatusHeader,
				navStatus3DFix,
				navClockHeader,
				"  iTOW 437000 clkBias 0 clkDrift 0 tAcc 23 fAcc 0\n",
				"UBX-NAV-TIMELS:\n",
				"  iTOW 376008000 version 0 reserved2 0 0 0 srcOfCurrLs 2\n",
				"  currLs 18 srcOfLsChange 2 lsChange 0 timeToLsEvent 77643210\n",
				"  dateOfLsGpsWn 2441 dateOfLsGpsDn 7 reserved2 0 0 0\n",
				"  valid x3\n",
			},
			maxOffset:      100,
			minOffset:      -100,
			wantSourceLost: false,
			wantOffset:     23,
			wantGPSStatus:  3,
			wantTimeLs: &ublox.TimeLs{
				SrcOfCurrLs:   2,
				CurrLs:        18,
				SrcOfLsChange: 2,
				TimeToLsEvent: 77643210,
				DateOfLsGpsWn: 2441,
				DateOfLsGpsDn: 7,
				Valid:         3,
			},
		},
		{
			// Exercises the end > len(lines) bounds clamp: fewer than 4 data
			// lines follow the TIMELS header. No NAV-CLOCK so offset stays at
			// the default sentinel (99999999), which is out of range.
			name: "truncated TIMELS does not panic",
			lines: []string{
				navStatusHeader,
				navStatus3DFix,
				"UBX-NAV-TIMELS:\n",
				"  iTOW 376008000 version 0 reserved2 0 0 0 srcOfCurrLs 2\n",
			},
			maxOffset:      100,
			minOffset:      -100,
			wantSourceLost: true,
			wantOffset:     99999999,
			wantGPSStatus:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var leapCh chan ublox.TimeLs
			if tt.wantTimeLs != nil {
				leapCh = make(chan ublox.TimeLs, 1)
				leap.LeapMgr = &leap.LeapManager{UbloxLsInd: leapCh}
				defer func() { leap.LeapMgr = nil }()
			}

			eventCh := make(chan event.Event, 10)
			g := &GPSD{
				processConfig: config.ProcessConfig{
					EventChannel: eventCh,
					ConfigName:   "ts2phc.0.config",
					ClockType:    event.GM,
					GMThreshold:  config.Threshold{Max: tt.maxOffset, Min: tt.minOffset},
				},
				gmInterface: "ens2f0",
			}

			g.processGNSSLines(tt.lines)

			assert.Equal(t, tt.wantSourceLost, g.sourceLost)
			assert.Equal(t, tt.wantOffset, g.offset)

			require.Len(t, eventCh, 1)
			ev := <-eventCh
			assert.Equal(t, event.GNSS, ev.Source)
			gnssData, ok := ev.Data.(*event.GNSSData)
			require.True(t, ok, "expected *event.GNSSData, got %T", ev.Data)
			assert.Equal(t, tt.wantGPSStatus, gnssData.GPSStatus)
			assert.Equal(t, tt.wantOffset, gnssData.Offset)
			assert.Equal(t, tt.wantSourceLost, gnssData.SourceLost)
			assert.Equal(t, "ens2f0", ev.IFace)

			if tt.wantTimeLs != nil {
				select {
				case ts := <-leapCh:
					assert.Equal(t, *tt.wantTimeLs, ts)
				default:
					t.Error("expected TimeLs to be sent to leap.LeapMgr.UbloxLsInd but channel was empty")
				}
			}
		})
	}
}
