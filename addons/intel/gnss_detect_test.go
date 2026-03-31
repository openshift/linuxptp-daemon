package intel

import (
	"errors"
	"os"
	"strings"
	"testing"

	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
)

func TestFindLeadingInterface(t *testing.T) {
	tests := []struct {
		name          string
		ts2phcConf    string
		expectedIface string
		expectedFound bool
	}{
		{
			name: "T-GM single interface, no nmea_serialport",
			ts2phcConf: `[nmea]
ts2phc.master 1
[global]
use_syslog 0
verbose 1
logging_level 7
ts2phc.pulsewidth 100000000
[ens7f0]
ts2phc.extts_polarity rising
ts2phc.extts_correction 0`,
			expectedIface: "ens7f0",
			expectedFound: true,
		},
		{
			name: "T-GM with nmea_serialport already set",
			ts2phcConf: `[nmea]
ts2phc.master 1
[global]
use_syslog 0
verbose 1
ts2phc.nmea_serialport /dev/gnss1
[ens7f0]
ts2phc.extts_polarity rising`,
			expectedIface: "",
			expectedFound: false,
		},
		{
			name: "T-BC config without [nmea] section",
			ts2phcConf: `[global]
use_syslog 0
verbose 1
logging_level 7
ts2phc.pulsewidth 100000000
[ens4f0]
ts2phc.extts_polarity rising
ts2phc.master 0
[ens8f0]
ts2phc.extts_polarity rising
ts2phc.master 0`,
			expectedIface: "",
			expectedFound: false,
		},
		{
			name: "multi-interface T-GM: one leading, two slaves",
			ts2phcConf: `[nmea]
ts2phc.master 1
[global]
use_syslog 0
verbose 1
ts2phc.pulsewidth 100000000
[ens7f0]
ts2phc.extts_polarity rising
ts2phc.extts_correction 0
[ens4f0]
ts2phc.extts_polarity rising
ts2phc.master 0
[ens8f0]
ts2phc.extts_polarity rising
ts2phc.master 0`,
			expectedIface: "ens7f0",
			expectedFound: true,
		},
		{
			name: "multi-interface T-GM: all without ts2phc.master 0 warns, returns first",
			ts2phcConf: `[nmea]
ts2phc.master 1
[global]
use_syslog 0
[ens7f0]
ts2phc.extts_polarity rising
[ens8f0]
ts2phc.extts_polarity rising`,
			expectedIface: "ens7f0",
			expectedFound: true,
		},
		{
			name: "T-GM with [nmea] but no interface sections",
			ts2phcConf: `[nmea]
ts2phc.master 1
[global]
use_syslog 0
verbose 1`,
			expectedIface: "",
			expectedFound: false,
		},
		{
			name:          "empty config",
			ts2phcConf:    "",
			expectedIface: "",
			expectedFound: false,
		},
		{
			name: "[nmea] without ts2phc.master still triggers auto-detection",
			ts2phcConf: `[nmea]
[global]
use_syslog 0
[ens7f0]
ts2phc.extts_polarity rising`,
			expectedIface: "ens7f0",
			expectedFound: true,
		},
		{
			name: "comments and blank lines are ignored",
			ts2phcConf: `# This is a comment
[nmea]
ts2phc.master 1

[global]
# serial port not set
use_syslog 0

[ens5f0]
ts2phc.extts_polarity rising`,
			expectedIface: "ens5f0",
			expectedFound: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iface, found := findLeadingInterface(tt.ts2phcConf)
			assert.Equal(t, tt.expectedFound, found, "found mismatch")
			assert.Equal(t, tt.expectedIface, iface, "interface mismatch")
		})
	}
}

func TestGnssDeviceFromInterface(t *testing.T) {
	tests := []struct {
		name        string
		iface       string
		setupMock   func(*MockFileSystem)
		expected    string
		expectError bool
	}{
		{
			name:  "single GNSS device found",
			iface: "ens7f0",
			setupMock: func(m *MockFileSystem) {
				m.ExpectReadDir("/sys/class/net/ens7f0/device/gnss",
					[]os.DirEntry{MockDirEntry{name: "gnss0"}}, nil)
			},
			expected:    "/dev/gnss0",
			expectError: false,
		},
		{
			name:  "multiple GNSS devices, returns first",
			iface: "ens7f0",
			setupMock: func(m *MockFileSystem) {
				m.ExpectReadDir("/sys/class/net/ens7f0/device/gnss",
					[]os.DirEntry{
						MockDirEntry{name: "gnss0"},
						MockDirEntry{name: "gnss1"},
					}, nil)
			},
			expected:    "/dev/gnss0",
			expectError: false,
		},
		{
			name:  "sysfs directory does not exist",
			iface: "ens7f0",
			setupMock: func(m *MockFileSystem) {
				m.ExpectReadDir("/sys/class/net/ens7f0/device/gnss",
					nil, errors.New("no such file or directory"))
			},
			expected:    "",
			expectError: true,
		},
		{
			name:  "sysfs directory is empty",
			iface: "ens7f0",
			setupMock: func(m *MockFileSystem) {
				m.ExpectReadDir("/sys/class/net/ens7f0/device/gnss",
					[]os.DirEntry{}, nil)
			},
			expected:    "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockFS, restoreFs := setupMockFS()
			defer restoreFs()
			tt.setupMock(mockFS)

			result, err := gnssDeviceFromInterface(tt.iface)
			if tt.expectError {
				assert.Error(t, err)
				assert.Empty(t, result)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
			mockFS.VerifyAllCalls(t)
		})
	}
}

func TestAutoDetectGNSSSerialPort(t *testing.T) {
	ts2phcConf := `[nmea]
ts2phc.master 1
[global]
use_syslog 0
verbose 1
[ens7f0]
ts2phc.extts_polarity rising`

	t.Run("patches config when GNSS device found", func(t *testing.T) {
		mockFS, restoreFs := setupMockFS()
		defer restoreFs()
		mockFS.ExpectReadDir("/sys/class/net/ens7f0/device/gnss",
			[]os.DirEntry{MockDirEntry{name: "gnss0"}}, nil)

		conf := ts2phcConf
		profile := &ptpv1.PtpProfile{Ts2PhcConf: &conf}
		autoDetectGNSSSerialPort(profile)

		assert.Contains(t, *profile.Ts2PhcConf, "ts2phc.nmea_serialport /dev/gnss0")
		lines := strings.Split(*profile.Ts2PhcConf, "\n")
		for i, line := range lines {
			if strings.TrimSpace(line) == "[global]" {
				assert.Equal(t, "ts2phc.nmea_serialport /dev/gnss0", lines[i+1])
				break
			}
		}
		mockFS.VerifyAllCalls(t)
	})

	t.Run("skips when nmea_serialport already set", func(t *testing.T) {
		confWithPort := `[nmea]
ts2phc.master 1
[global]
ts2phc.nmea_serialport /dev/gnss1
[ens7f0]
ts2phc.extts_polarity rising`
		conf := confWithPort
		profile := &ptpv1.PtpProfile{Ts2PhcConf: &conf}
		autoDetectGNSSSerialPort(profile)

		assert.Equal(t, confWithPort, *profile.Ts2PhcConf, "config should be unchanged")
	})

	t.Run("skips when Ts2PhcConf is nil", func(t *testing.T) {
		profile := &ptpv1.PtpProfile{Ts2PhcConf: nil}
		autoDetectGNSSSerialPort(profile)
		assert.Nil(t, profile.Ts2PhcConf)
	})

	t.Run("skips when sysfs lookup fails", func(t *testing.T) {
		mockFS, restoreFs := setupMockFS()
		defer restoreFs()
		mockFS.ExpectReadDir("/sys/class/net/ens7f0/device/gnss",
			nil, errors.New("no such file or directory"))

		conf := ts2phcConf
		profile := &ptpv1.PtpProfile{Ts2PhcConf: &conf}
		autoDetectGNSSSerialPort(profile)

		assert.NotContains(t, *profile.Ts2PhcConf, "ts2phc.nmea_serialport")
		mockFS.VerifyAllCalls(t)
	})
}
