package daemon

import (
	"context"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
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
