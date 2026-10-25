package ublox

import (
	"errors"
	"fmt"
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

type ethernetFixtureDevice struct {
	iface        string
	pciAddress   string
	slot         string
	slotMapping  bool
	permanentMAC string
	vendorID     string
	deviceID     string
}

func setupEthernetSysfsFixture(t *testing.T, devices []ethernetFixtureDevice, gnss map[string][]os.DirEntry) {
	t.Helper()

	oldNetClassPath := netClassSysfsPath
	oldPCISlotsPath := pciSlotsSysfsPath
	oldReadDir := ReadDir
	oldPermanentMACAddress := getPermanentMACAddress
	root := t.TempDir()
	netClassSysfsPath = filepath.Join(root, "sys", "class", "net")
	pciSlotsSysfsPath = filepath.Join(root, "sys", "bus", "pci", "slots")
	if err := os.MkdirAll(netClassSysfsPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pciSlotsSysfsPath, 0o755); err != nil {
		t.Fatal(err)
	}

	macAddresses := make(map[string]string, len(devices))
	gnssDirectories := make(map[string][]os.DirEntry, len(gnss))
	for iface, entries := range gnss {
		gnssDirectories[fmt.Sprintf(GNSSDeviceSysfsTemplate, iface)] = entries
	}
	for _, device := range devices {
		ifacePath := filepath.Join(netClassSysfsPath, device.iface)
		pciPath := filepath.Join(root, "sys", "devices", "pci", device.pciAddress)
		if err := os.MkdirAll(ifacePath, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(pciPath, 0o755); err != nil {
			t.Fatal(err)
		}
		for attribute, value := range map[string]string{
			"vendor": device.vendorID,
			"device": device.deviceID,
		} {
			if value != "" {
				if err := os.WriteFile(filepath.Join(pciPath, attribute), []byte("0x"+value+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := os.Symlink(pciPath, filepath.Join(ifacePath, "device")); err != nil {
			t.Fatal(err)
		}
		if device.slot != "" {
			if device.slotMapping {
				slotPath := filepath.Join(pciSlotsSysfsPath, device.slot)
				if err := os.MkdirAll(slotPath, 0o755); err != nil {
					t.Fatal(err)
				}
				address := device.pciAddress[:strings.LastIndex(device.pciAddress, ".")]
				if err := os.WriteFile(filepath.Join(slotPath, "address"), []byte(address+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				firmwarePath := filepath.Join(pciPath, "firmware_node")
				if err := os.MkdirAll(firmwarePath, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(firmwarePath, "sun"), []byte(device.slot+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		macAddresses[device.iface] = device.permanentMAC
	}

	ReadDir = func(path string) ([]os.DirEntry, error) {
		if entries, ok := gnssDirectories[path]; ok {
			return entries, nil
		}
		return os.ReadDir(path)
	}
	getPermanentMACAddress = func(iface string) (string, error) {
		address, ok := macAddresses[iface]
		if !ok || address == "" {
			return "", fmt.Errorf("no permanent address for %s", iface)
		}
		return address, nil
	}
	t.Cleanup(func() {
		netClassSysfsPath = oldNetClassPath
		pciSlotsSysfsPath = oldPCISlotsPath
		ReadDir = oldReadDir
		getPermanentMACAddress = oldPermanentMACAddress
	})
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

func TestReadPermanentMACAddress(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		commandErr error
		want       string
		wantErr    string
	}{
		{
			name:   "parses permanent address",
			output: "Permanent address: 00:11:22:AA:BB:CC\n",
			want:   "00:11:22:aa:bb:cc",
		},
		{
			name:       "reports command failure",
			commandErr: errors.New("ethtool unavailable"),
			wantErr:    "cannot read permanent MAC address",
		},
		{
			name:    "reports missing address",
			output:  "Settings for eno1:\n",
			wantErr: "ethtool returned no permanent MAC address",
		},
		{
			name:    "rejects malformed address",
			output:  "Permanent address: invalid\n",
			wantErr: "invalid permanent MAC address reported",
		},
	}

	originalCommandOutput := commandOutput
	defer func() { commandOutput = originalCommandOutput }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commandOutput = func(name string, args ...string) ([]byte, error) {
				assert.Equal(t, "ethtool", name)
				assert.Equal(t, []string{"-P", "eno1"}, args)
				return []byte(tt.output), tt.commandErr
			}

			got, err := readPermanentMACAddress("eno1")
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
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

func TestGNSSDeviceFromEthernetDevice(t *testing.T) {
	t.Run("selects GNSS device by PCI address", func(t *testing.T) {
		setupEthernetSysfsFixture(t, []ethernetFixtureDevice{{
			iface: "eno8703", pciAddress: "0000:86:00.0", slot: "2",
		}}, map[string][]os.DirEntry{
			"eno8703": {mockDirEntry{name: "gnss0"}},
		})

		device, err := GNSSDeviceFromEthernetDevice("", "86:00.0", "", "", "", "")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("selects GNSS device by interface name", func(t *testing.T) {
		restore := setupReadDirMock(
			map[string][]os.DirEntry{
				"/sys/class/net/eno8703/device/gnss": {mockDirEntry{name: "gnss0"}},
			}, nil,
		)
		defer restore()

		device, err := GNSSDeviceFromEthernetDevice("eno8703", "", "", "", "", "")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("matches all supplied selectors together", func(t *testing.T) {
		setupEthernetSysfsFixture(t, []ethernetFixtureDevice{
			{iface: "eno8703", pciAddress: "0000:86:00.0", slot: "2", permanentMAC: "00:11:22:aa:bb:cc", vendorID: "8086", deviceID: "159b"},
			{iface: "eno8704", pciAddress: "0000:87:00.0", slot: "3", permanentMAC: "00:11:22:aa:bb:dd", vendorID: "8086", deviceID: "1592"},
		}, map[string][]os.DirEntry{
			"eno8703": {mockDirEntry{name: "gnss0"}},
			"eno8704": {mockDirEntry{name: "gnss1"}},
		})

		device, err := GNSSDeviceFromEthernetDevice("eno8703", "86:00.0", "00:11:22:AA:BB:CC", "02", "0x8086", "159B")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("selects by PCI vendor and device IDs", func(t *testing.T) {
		setupEthernetSysfsFixture(t, []ethernetFixtureDevice{
			{iface: "eno8703", pciAddress: "0000:86:00.0", slot: "2", vendorID: "8086", deviceID: "159b"},
			{iface: "eno8704", pciAddress: "0000:87:00.0", slot: "3", vendorID: "8086", deviceID: "1592"},
			{iface: "eno8705", pciAddress: "0000:88:00.0", slot: "4", vendorID: "1af4", deviceID: "159b"},
		}, map[string][]os.DirEntry{
			"eno8703": {mockDirEntry{name: "gnss0"}},
			"eno8704": {mockDirEntry{name: "gnss1"}},
			"eno8705": {mockDirEntry{name: "gnss2"}},
		})

		device, err := GNSSDeviceFromEthernetDevice("", "", "", "", "0x8086", "159B")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("selects by vendor ID alone", func(t *testing.T) {
		setupEthernetSysfsFixture(t, []ethernetFixtureDevice{
			{iface: "eno8703", pciAddress: "0000:86:00.0", slot: "2", vendorID: "8086", deviceID: "159b"},
			{iface: "eno8704", pciAddress: "0000:87:00.0", slot: "3", vendorID: "1af4", deviceID: "159b"},
		}, map[string][]os.DirEntry{
			"eno8703": {mockDirEntry{name: "gnss0"}},
			"eno8704": {mockDirEntry{name: "gnss1"}},
		})

		device, err := GNSSDeviceFromEthernetDevice("", "", "", "", "8086", "")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("selects by device ID alone", func(t *testing.T) {
		setupEthernetSysfsFixture(t, []ethernetFixtureDevice{
			{iface: "eno8703", pciAddress: "0000:86:00.0", slot: "2", vendorID: "8086", deviceID: "159b"},
			{iface: "eno8704", pciAddress: "0000:87:00.0", slot: "3", vendorID: "8086", deviceID: "1592"},
		}, map[string][]os.DirEntry{
			"eno8703": {mockDirEntry{name: "gnss0"}},
			"eno8704": {mockDirEntry{name: "gnss1"}},
		})

		device, err := GNSSDeviceFromEthernetDevice("", "", "", "", "", "159b")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("lists matching PCI slots when IDs are ambiguous", func(t *testing.T) {
		setupEthernetSysfsFixture(t, []ethernetFixtureDevice{
			{iface: "eno8703", pciAddress: "0000:86:00.0", slot: "2", vendorID: "8086", deviceID: "159b"},
			{iface: "eno8704", pciAddress: "0000:87:00.0", slot: "3", vendorID: "8086", deviceID: "159b"},
		}, map[string][]os.DirEntry{
			"eno8703": {mockDirEntry{name: "gnss0"}},
			"eno8704": {mockDirEntry{name: "gnss1"}},
		})

		_, err := GNSSDeviceFromEthernetDevice("", "", "", "", "8086", "159b")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "slot 2")
		assert.Contains(t, err.Error(), "slot 3")
		assert.Contains(t, err.Error(), "specify slot")

		device, err := GNSSDeviceFromEthernetDevice("", "", "", "3", "8086", "159b")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss1", device)
	})

	t.Run("rejects malformed PCI addresses", func(t *testing.T) {
		for _, address := range []string{"foo", "86:00", "0000:gg:00.0", "0000:86:00.8"} {
			_, err := GNSSDeviceFromEthernetDevice("", address, "", "", "", "")
			assert.ErrorContains(t, err, "invalid PCI address", "address %q", address)
		}
	})

	t.Run("rejects invalid PCI vendor and device IDs", func(t *testing.T) {
		_, err := GNSSDeviceFromEthernetDevice("", "", "", "", "not-hex", "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid PCI vendor ID")

		_, err = GNSSDeviceFromEthernetDevice("", "", "", "", "", "not-hex")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid PCI device ID")
	})

	t.Run("selects by firmware-reported slot", func(t *testing.T) {
		setupEthernetSysfsFixture(t, []ethernetFixtureDevice{
			{iface: "eno8703", pciAddress: "0000:86:00.0", slot: "2"},
			{iface: "eno8704", pciAddress: "0000:87:00.0", slot: "3"},
		}, map[string][]os.DirEntry{
			"eno8703": {mockDirEntry{name: "gnss0"}},
			"eno8704": {mockDirEntry{name: "gnss1"}},
		})

		device, err := GNSSDeviceFromEthernetDevice("", "", "", "2", "", "")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("resolves slot from PCI slot address mapping", func(t *testing.T) {
		setupEthernetSysfsFixture(t, []ethernetFixtureDevice{{
			iface: "eno8703", pciAddress: "0000:86:00.0", slot: "2", slotMapping: true,
		}}, map[string][]os.DirEntry{
			"eno8703": {mockDirEntry{name: "gnss0"}},
		})

		device, err := GNSSDeviceFromEthernetDevice("", "", "", "2", "", "")
		assert.NoError(t, err)
		assert.Equal(t, "/dev/gnss0", device)
	})

	t.Run("rejects invalid permanent MAC address", func(t *testing.T) {
		_, err := GNSSDeviceFromEthernetDevice("", "", "not-a-mac", "", "", "")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid permanent MAC address")
	})
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
