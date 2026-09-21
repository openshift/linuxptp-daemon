package ublox

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type mockDirEntry struct {
	name  string
	isDir bool
}

func (m mockDirEntry) Name() string               { return m.name }
func (m mockDirEntry) IsDir() bool                { return m.isDir }
func (m mockDirEntry) Type() fs.FileMode          { return 0 }
func (m mockDirEntry) Info() (fs.FileInfo, error) { return mockFileInfo{name: m.name}, nil }

type mockFileInfo struct{ name string }

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return 0 }
func (m mockFileInfo) Mode() fs.FileMode  { return 0 }
func (m mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m mockFileInfo) IsDir() bool        { return false }
func (m mockFileInfo) Sys() interface{}   { return nil }

const testSysfsPath = "/sys/class/net/ens7f0/device/gnss"

func setupReadDirMock(entries map[string][]os.DirEntry, errs map[string]error) func() {
	orig := ReadDir
	ReadDir = func(name string) ([]os.DirEntry, error) {
		if errs != nil {
			if err, ok := errs[name]; ok {
				return nil, err
			}
		}
		if entries != nil {
			if e, ok := entries[name]; ok {
				return e, nil
			}
		}
		return nil, errors.New("not found")
	}
	return func() { ReadDir = orig }
}

func makeUSBTTYFixture(t *testing.T, ttyNames ...string) string {
	t.Helper()
	return makeUSBTTYFixtureAtPaths(t, map[string][]string{"1-4": ttyNames})
}

func makeUSBTTYFixtureAtPaths(t *testing.T, devices map[string][]string) string {
	t.Helper()

	root := t.TempDir()
	ttyClassPath := filepath.Join(root, "sys", "class", "tty")
	usbBusPath := filepath.Join(root, "sys", "bus", "usb")
	for topologyPath, ttyNames := range devices {
		busNumber := strings.SplitN(topologyPath, "-", 2)[0]
		usbDevicePath := filepath.Join(root, "sys", "devices", "usb"+busNumber, topologyPath)
		usbInterfacePath := filepath.Join(usbDevicePath, topologyPath+":1.0")

		for _, path := range []string{ttyClassPath, usbBusPath, usbDevicePath, usbInterfacePath} {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(usbDevicePath, "idVendor"), []byte("1546\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(usbDevicePath, "idProduct"), []byte("01a9\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(usbBusPath, filepath.Join(usbDevicePath, "subsystem")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(usbBusPath, filepath.Join(usbInterfacePath, "subsystem")); err != nil {
			t.Fatal(err)
		}

		for _, ttyName := range ttyNames {
			ttyPath := filepath.Join(ttyClassPath, ttyName)
			if err := os.MkdirAll(ttyPath, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(usbInterfacePath, filepath.Join(ttyPath, "device")); err != nil {
				t.Fatal(err)
			}
		}
	}
	return ttyClassPath
}

func TestNormalizeUSBID(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "four digit ID", in: "1546", want: "1546"},
		{name: "short ID", in: "1a9", want: "01a9"},
		{name: "0x prefix", in: "0x01A9", want: "01a9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeUSBID(tt.in)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}

	_, err := normalizeUSBID("not-hex")
	assert.Error(t, err)
}

func makeACPITTYFixture(t *testing.T, ttyNames ...string) string {
	t.Helper()

	root := t.TempDir()
	ttyClassPath := filepath.Join(root, "sys", "class", "tty")
	acpiTTYPath := filepath.Join(root, "sys", "devices", "platform", "INTC10EE:00", "serial8250", "tty")
	if err := os.MkdirAll(acpiTTYPath, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, ttyName := range ttyNames {
		ttyPath := filepath.Join(ttyClassPath, ttyName)
		if err := os.MkdirAll(ttyPath, 0o755); err != nil {
			t.Fatal(err)
		}
		devicePath := filepath.Join(acpiTTYPath, ttyName)
		if err := os.MkdirAll(devicePath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(devicePath, filepath.Join(ttyPath, "device")); err != nil {
			t.Fatal(err)
		}
	}
	return ttyClassPath
}

func TestGNSSDeviceFromACPIDevice(t *testing.T) {
	t.Run("finds tty by ACPI HID and UID", func(t *testing.T) {
		ttyClassPath := makeACPITTYFixture(t, "ttyS2")

		device, err := findTTYFromACPIDevice(ttyClassPath, "intc10ee", "00")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/ttyS2", device)
	})

	t.Run("matches any ACPI UID when UID is omitted", func(t *testing.T) {
		ttyClassPath := makeACPITTYFixture(t, "ttyS2")

		device, err := findTTYFromACPIDevice(ttyClassPath, "INTC10EE", "")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/ttyS2", device)
	})

	t.Run("returns an error when no tty matches", func(t *testing.T) {
		ttyClassPath := makeACPITTYFixture(t, "ttyS2")

		_, err := findTTYFromACPIDevice(ttyClassPath, "INTC10EE", "01")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no tty device found")
	})

	t.Run("returns an error when multiple ttys match", func(t *testing.T) {
		ttyClassPath := makeACPITTYFixture(t, "ttyS2", "ttyS3")

		_, err := findTTYFromACPIDevice(ttyClassPath, "INTC10EE", "00")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "/dev/ttyS2")
		assert.Contains(t, err.Error(), "/dev/ttyS3")
	})
}

func TestGNSSDeviceFromUSB(t *testing.T) {
	t.Run("finds tty by vendor and product", func(t *testing.T) {
		ttyClassPath := makeUSBTTYFixture(t, "ttyACM0")

		device, err := findTTYFromUSBDevice(ttyClassPath, "1546", "01A9", "")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/ttyACM0", device)
	})

	t.Run("returns an error when no tty matches", func(t *testing.T) {
		ttyClassPath := makeUSBTTYFixture(t, "ttyACM0")

		_, err := findTTYFromUSBDevice(ttyClassPath, "1546", "0001", "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no tty device found")
	})

	t.Run("returns an error when USB device exposes multiple ttys", func(t *testing.T) {
		ttyClassPath := makeUSBTTYFixture(t, "ttyACM0", "ttyACM1")

		_, err := findTTYFromUSBDevice(ttyClassPath, "1546", "01a9", "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "/dev/ttyACM0")
		assert.Contains(t, err.Error(), "/dev/ttyACM1")
		assert.Contains(t, err.Error(), "matched USB paths: 1-4")
	})

	t.Run("ambiguity lists every matching USB topology path", func(t *testing.T) {
		ttyClassPath := makeUSBTTYFixtureAtPaths(t, map[string][]string{
			"1-4":   {"ttyACM0"},
			"2-1.4": {"ttyACM1"},
		})

		_, err := findTTYFromUSBDevice(ttyClassPath, "1546", "01a9", "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "matched USB paths: 1-4, 2-1.4")
	})

	t.Run("topology path selects one of multiple identical devices", func(t *testing.T) {
		ttyClassPath := makeUSBTTYFixtureAtPaths(t, map[string][]string{
			"1-4":   {"ttyACM0"},
			"2-1.4": {"ttyACM1"},
		})

		device, err := findTTYFromUSBDevice(ttyClassPath, "1546", "01a9", "2-1.4")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/ttyACM1", device)
	})

	t.Run("path with no matching device returns an error", func(t *testing.T) {
		ttyClassPath := makeUSBTTYFixture(t, "ttyACM0")

		_, err := findTTYFromUSBDevice(ttyClassPath, "1546", "01a9", "1-5")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), `at path "1-5"`)
	})

	t.Run("validates USB topology path syntax", func(t *testing.T) {
		assert.False(t, validUSBTopologyPath("../1-4"))
		assert.False(t, validUSBTopologyPath("1-"))
		assert.True(t, validUSBTopologyPath("2-1.4"))
	})
}

func TestGNSSDeviceFromInterface(t *testing.T) {
	t.Run("single device", func(t *testing.T) {
		restore := setupReadDirMock(
			map[string][]os.DirEntry{
				testSysfsPath: {mockDirEntry{name: "gnss0"}},
			}, nil,
		)
		defer restore()

		device, err := GNSSDeviceFromInterface("ens7f0")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("multiple devices returns first sorted", func(t *testing.T) {
		restore := setupReadDirMock(
			map[string][]os.DirEntry{
				testSysfsPath: {
					mockDirEntry{name: "gnss1"},
					mockDirEntry{name: "gnss0"},
				},
			}, nil,
		)
		defer restore()

		device, err := GNSSDeviceFromInterface("ens7f0")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("sysfs directory does not exist", func(t *testing.T) {
		restore := setupReadDirMock(nil,
			map[string]error{
				testSysfsPath: errors.New("no such file or directory"),
			},
		)
		defer restore()

		_, err := GNSSDeviceFromInterface("ens7f0")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no GNSS device found")
	})

	t.Run("sysfs directory is empty", func(t *testing.T) {
		restore := setupReadDirMock(
			map[string][]os.DirEntry{
				testSysfsPath: {},
			}, nil,
		)
		defer restore()

		_, err := GNSSDeviceFromInterface("ens7f0")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})
}
