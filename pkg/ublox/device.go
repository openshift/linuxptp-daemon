package ublox

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/golang/glog"
)

const (
	// GNSSDeviceSysfsTemplate is the sysfs path template for finding GNSS
	// devices attached to a network interface.
	GNSSDeviceSysfsTemplate = "/sys/class/net/%s/device/gnss"

	// ttyClassSysfsPath contains the sysfs class entries for tty devices.
	ttyClassSysfsPath = "/sys/class/tty"
)

// ReadDir is the function used to read sysfs directories.
// Replace in tests to mock filesystem access.
var ReadDir = os.ReadDir

// GNSSDeviceFromInterface resolves the GNSS TTY device path for a given
// network interface by reading the sysfs directory
// /sys/class/net/<iface>/device/gnss/.
func GNSSDeviceFromInterface(iface string) (string, error) {
	glog.Infof("Looking for GNSS device associated with iface %s", iface)
	gnssDir := fmt.Sprintf(GNSSDeviceSysfsTemplate, iface)
	entries, err := ReadDir(gnssDir)
	if err != nil {
		return "", fmt.Errorf("no GNSS device found for interface %s: %w", iface, err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("GNSS sysfs directory %s is empty", gnssDir)
	}
	if len(entries) > 1 {
		// Sort for deterministic selection when multiple devices exist
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name() < entries[j].Name()
		})
		glog.Warningf("multiple GNSS devices found for %s, using %s", iface, entries[0].Name())
	}
	result := fmt.Sprintf("/dev/%s", entries[0].Name())
	glog.Infof("Detected GNSS device %s", result)
	return result, nil
}

// GNSSDeviceFromUSB resolves the tty device exposed by a USB device with the
// given hexadecimal vendor and product IDs, optionally constrained by its USB
// bus-port topology path. It enumerates tty class devices and walks their sysfs
// ancestry, rather than relying on an unstable tty name such as ttyACM0.
func GNSSDeviceFromUSB(vendor, product, topologyPath string) (string, error) {
	rawVendor, rawProduct := vendor, product
	vendor, err := normalizeUSBID(vendor)
	if err != nil {
		return "", fmt.Errorf("invalid USB vendor ID %q: %w", rawVendor, err)
	}
	product, err = normalizeUSBID(product)
	if err != nil {
		return "", fmt.Errorf("invalid USB product ID %q: %w", rawProduct, err)
	}
	topologyPath = strings.TrimSpace(topologyPath)
	if topologyPath != "" && !validUSBTopologyPath(topologyPath) {
		return "", fmt.Errorf("invalid USB topology path %q", topologyPath)
	}

	return findTTYFromUSBDevice(ttyClassSysfsPath, vendor, product, topologyPath)
}

func findTTYFromUSBDevice(ttyClassPath, vendor, product, topologyPath string) (string, error) {
	glog.Infof("Looking for GNSS tty exposed by USB device %s:%s at path %q", vendor, product, topologyPath)
	entries, err := ReadDir(ttyClassPath)
	if err != nil {
		return "", fmt.Errorf("cannot enumerate tty devices: %w", err)
	}

	var candidates []string
	matchedPathSet := make(map[string]struct{})
	for _, entry := range entries {
		devicePath := filepath.Join(ttyClassPath, entry.Name(), "device")
		resolvedDevicePath, symlinkErr := filepath.EvalSymlinks(devicePath)
		if symlinkErr != nil {
			// Virtual tty devices and stale class entries may not have a
			// resolvable device path.
			continue
		}
		if matchedPath, ok := usbDeviceMatchPath(resolvedDevicePath, vendor, product, topologyPath); ok {
			candidates = append(candidates, filepath.Join("/dev", entry.Name()))
			matchedPathSet[matchedPath] = struct{}{}
		}
	}

	sort.Strings(candidates)
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no tty device found for USB device %s:%s at path %q", vendor, product, topologyPath)
	case 1:
		glog.Infof("Detected GNSS device %s", candidates[0])
		return candidates[0], nil
	default:
		matchedPaths := make([]string, 0, len(matchedPathSet))
		for matchedPath := range matchedPathSet {
			matchedPaths = append(matchedPaths, matchedPath)
		}
		sort.Strings(matchedPaths)
		message := fmt.Sprintf("multiple tty devices found for USB device %s:%s at path %q: %s; matched USB paths: %s",
			vendor, product, topologyPath, strings.Join(candidates, ", "), strings.Join(matchedPaths, ", "))
		glog.Errorf("%s", message)
		return "", fmt.Errorf("%s", message)
	}
}

// usbDeviceMatchPath walks from a tty's sysfs device to its parents, looking
// for a USB device node with matching IDs and, when provided, its bus-port path.
// It returns the matched USB topology path for diagnostic output.
func usbDeviceMatchPath(devicePath, vendor, product, topologyPath string) (string, bool) {
	for sysfsPath := devicePath; sysfsPath != "." && sysfsPath != string(filepath.Separator); sysfsPath = filepath.Dir(sysfsPath) {
		subsystemPath, err := filepath.EvalSymlinks(filepath.Join(sysfsPath, "subsystem"))
		if err == nil && filepath.Base(subsystemPath) == "usb" {
			matchedPath := filepath.Base(sysfsPath)
			if topologyPath != "" && matchedPath != topologyPath {
				continue
			}
			actualVendor, vendorErr := os.ReadFile(filepath.Join(sysfsPath, "idVendor"))
			actualProduct, productErr := os.ReadFile(filepath.Join(sysfsPath, "idProduct"))
			if vendorErr == nil && productErr == nil &&
				strings.EqualFold(strings.TrimSpace(string(actualVendor)), vendor) &&
				strings.EqualFold(strings.TrimSpace(string(actualProduct)), product) {
				return matchedPath, true
			}
		}
	}
	return "", false
}

func validUSBTopologyPath(path string) bool {
	parts := strings.Split(path, "-")
	if len(parts) != 2 || !decimalUSBPathComponent(parts[0]) {
		return false
	}
	for _, port := range strings.Split(parts[1], ".") {
		if !decimalUSBPathComponent(port) {
			return false
		}
	}
	return true
}

func decimalUSBPathComponent(value string) bool {
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func normalizeUSBID(id string) (string, error) {
	id = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(id), "0x"))
	if id == "" || len(id) > 4 {
		return "", fmt.Errorf("must be one to four hexadecimal digits")
	}
	value, err := strconv.ParseUint(id, 16, 16)
	if err != nil {
		return "", fmt.Errorf("must be hexadecimal: %w", err)
	}
	return fmt.Sprintf("%04x", value), nil
}
