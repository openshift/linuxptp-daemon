package daemon

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/config"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/event"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		input          string
		maxOffset      int64
		minOffset      int64
		wantSourceLost bool
		wantOffset     int64
		wantGPSStatus  int64
	}{
		{
			name: "locked with good fix and offset in range",
			input: "UBX-NAV-STATUS:\n" +
				"  iTOW 437000 gpsFix 3 flags 0xdd fixStat 0 flags2 0x08\n" +
				"UBX-NAV-CLOCK:\n" +
				"  iTOW 437000 clkBias 42 clkDrift 0 tAcc 23 fAcc 0\n",
			maxOffset:      100,
			minOffset:      -100,
			wantSourceLost: false,
			wantOffset:     23,
			wantGPSStatus:  3,
		},
		{
			name: "freerun with no fix",
			input: "UBX-NAV-STATUS:\n" +
				"  iTOW 437000 gpsFix 0 flags 0x00 fixStat 0 flags2 0x00\n" +
				"UBX-NAV-CLOCK:\n" +
				"  iTOW 437000 clkBias 42 clkDrift 0 tAcc 50 fAcc 0\n",
			maxOffset:      100,
			minOffset:      -100,
			wantSourceLost: true,
			wantOffset:     50,
			wantGPSStatus:  0,
		},
		{
			name: "freerun when offset out of range despite good fix",
			input: "UBX-NAV-STATUS:\n" +
				"  iTOW 437000 gpsFix 3 flags 0xdd fixStat 0 flags2 0x08\n" +
				"UBX-NAV-CLOCK:\n" +
				"  iTOW 437000 clkBias 42 clkDrift 0 tAcc 5000 fAcc 0\n",
			maxOffset:      100,
			minOffset:      -100,
			wantSourceLost: true,
			wantOffset:     5000,
			wantGPSStatus:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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

			g.processGNSSLines(strings.NewReader(tt.input))

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
		})
	}
}
