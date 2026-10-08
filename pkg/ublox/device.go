package ublox

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

var (
	pciAddressPattern = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-7]$`)

	// netClassSysfsPath contains the sysfs class entries for network devices.
	netClassSysfsPath = "/sys/class/net"

	// pciSlotsSysfsPath contains firmware-reported PCI hotplug slot mappings.
	pciSlotsSysfsPath = "/sys/bus/pci/slots"

	// ReadDir wraps OS calls, and readFile and evalSymlinks allow sysfs access
	// to be controlled in tests.
	ReadDir      = os.ReadDir
	readFile     = os.ReadFile
	evalSymlinks = filepath.EvalSymlinks

	// commandOutput wraps command execution so ethtool behavior can be tested
	// without requiring a physical NIC.
	commandOutput = func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).Output()
	}

	// getPermanentMACAddress returns the permanent hardware address reported
	// by ethtool. Replace in tests to avoid requiring a physical NIC.
	getPermanentMACAddress = readPermanentMACAddress
)

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

// GNSSDeviceFromEthernetDevice resolves a GNSS device attached to an Ethernet
// device. Every supplied selector is applied as an AND criterion. A name-only
// selector uses a direct interface lookup; selectors that identify hardware
// are resolved against the interface's sysfs device information.
func GNSSDeviceFromEthernetDevice(name, pciAddress, permanentMAC, slot, vendorID, deviceID string) (string, error) {
	if name == "" && pciAddress == "" && permanentMAC == "" && slot == "" && vendorID == "" && deviceID == "" {
		return "", fmt.Errorf("EthernetDevice has no selection criteria")
	}

	if pciAddress != "" {
		var err error
		pciAddress, err = normalizePCIAddress(pciAddress)
		if err != nil {
			return "", err
		}
	}
	if permanentMAC != "" {
		address, err := net.ParseMAC(strings.TrimSpace(permanentMAC))
		if err != nil || len(address) != 6 {
			return "", fmt.Errorf("invalid permanent MAC address %q", permanentMAC)
		}
		permanentMAC = strings.ToLower(address.String())
	}
	if slot != "" {
		normalized, err := normalizePCIPosition(slot)
		if err != nil {
			return "", fmt.Errorf("invalid PCI slot ID %q: %w", slot, err)
		}
		slot = normalized
	}
	if vendorID != "" {
		rawVendorID := vendorID
		var err error
		vendorID, err = normalizePCIID(vendorID)
		if err != nil {
			return "", fmt.Errorf("invalid PCI vendor ID %q: %w", rawVendorID, err)
		}
	}
	if deviceID != "" {
		rawDeviceID := deviceID
		var err error
		deviceID, err = normalizePCIID(deviceID)
		if err != nil {
			return "", fmt.Errorf("invalid PCI device ID %q: %w", rawDeviceID, err)
		}
	}

	if name != "" && pciAddress == "" && permanentMAC == "" && slot == "" && vendorID == "" && deviceID == "" {
		return GNSSDeviceFromInterface(name)
	}

	entries, err := ReadDir(netClassSysfsPath)
	if err != nil {
		return "", fmt.Errorf("cannot enumerate Ethernet devices: %w", err)
	}
	interfaces := make([]string, 0, len(entries))
	for _, entry := range entries {
		if ethernetInterfaceMatches(entry.Name(), name, pciAddress, permanentMAC, slot, vendorID, deviceID) {
			interfaces = append(interfaces, entry.Name())
		}
	}
	return gnssDeviceFromEthernetInterfaces(interfaces)
}

func ethernetInterfaceMatches(iface, name, pciAddress, permanentMAC, slot, vendorID, deviceID string) bool {
	if name != "" && iface != name {
		return false
	}
	if pciAddress == "" && permanentMAC == "" && slot == "" && vendorID == "" && deviceID == "" {
		return true
	}

	devicePath, err := evalSymlinks(filepath.Join(netClassSysfsPath, iface, "device"))
	if err != nil {
		return false
	}
	if pciAddress != "" && !strings.EqualFold(filepath.Base(devicePath), pciAddress) {
		return false
	}
	if permanentMAC != "" {
		actual, macErr := getPermanentMACAddress(iface)
		if macErr != nil || !strings.EqualFold(actual, permanentMAC) {
			return false
		}
	}
	if slot != "" {
		actualSlot, slotErr := pciSlotID(devicePath)
		if slotErr != nil || actualSlot != slot {
			return false
		}
	}
	if vendorID != "" && !pciIDMatches(devicePath, "vendor", vendorID) {
		return false
	}
	if deviceID != "" && !pciIDMatches(devicePath, "device", deviceID) {
		return false
	}
	return true
}

func pciIDMatches(devicePath, attribute, expected string) bool {
	actual, err := readFile(filepath.Join(devicePath, attribute))
	return err == nil && strings.EqualFold(strings.TrimSpace(string(actual)), "0x"+expected)
}

func readPermanentMACAddress(iface string) (string, error) {
	output, err := commandOutput("ethtool", "-P", iface)
	if err != nil {
		return "", fmt.Errorf("cannot read permanent MAC address for %s: %w", iface, err)
	}
	const prefix = "Permanent address:"
	for _, line := range strings.Split(string(output), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			address, parseErr := net.ParseMAC(strings.TrimSpace(value))
			if parseErr != nil || len(address) != 6 {
				return "", fmt.Errorf("invalid permanent MAC address reported for %s", iface)
			}
			return strings.ToLower(address.String()), nil
		}
	}
	return "", fmt.Errorf("ethtool returned no permanent MAC address for %s", iface)
}

// pciSlotID returns the slot number used by systemd's PCI slot-based naming.
// Newer systems expose the ACPI _SUN value through firmware_node/sun. Older
// systems expose slot-to-device mappings below /sys/bus/pci/slots.
func pciSlotID(devicePath string) (string, error) {
	for path := devicePath; path != "." && path != string(filepath.Separator); path = filepath.Dir(path) {
		if value, readErr := readFile(filepath.Join(path, "firmware_node", "sun")); readErr == nil {
			if slot, parseErr := normalizePCIPosition(string(value)); parseErr == nil {
				return slot, nil
			}
		}

		pciAddress := filepath.Base(path)
		if !strings.Contains(pciAddress, ":") || !strings.Contains(pciAddress, ".") {
			continue
		}
		entries, err := ReadDir(pciSlotsSysfsPath)
		if err != nil {
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			address, readErr := readFile(filepath.Join(pciSlotsSysfsPath, entry.Name(), "address"))
			if readErr != nil {
				continue
			}
			slotAddress := strings.TrimSpace(string(address))
			if strings.Count(slotAddress, ":") == 1 {
				slotAddress = "0000:" + slotAddress
			}
			if slotAddress != "" && strings.HasPrefix(pciAddress, slotAddress) {
				if slot, parseErr := normalizePCIPosition(entry.Name()); parseErr == nil {
					return slot, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no firmware-reported PCI slot for %s", filepath.Base(devicePath))
}

// normalizePCIPosition canonicalizes a decimal PCI slot or function number.
func normalizePCIPosition(position string) (string, error) {
	value, err := strconv.ParseUint(strings.TrimSpace(position), 10, 32)
	if err != nil {
		return "", fmt.Errorf("must be a non-negative decimal number")
	}
	return strconv.FormatUint(value, 10), nil
}

type ethernetGNSSCandidate struct {
	iface  string
	device string
	slot   string
}

func gnssDeviceFromEthernetInterfaces(interfaces []string) (string, error) {
	sort.Strings(interfaces)
	var candidates []ethernetGNSSCandidate
	for _, iface := range interfaces {
		device, err := GNSSDeviceFromInterface(iface)
		if err != nil {
			continue
		}
		candidate := ethernetGNSSCandidate{iface: iface, device: device, slot: "unknown"}
		if devicePath, symlinkErr := evalSymlinks(filepath.Join(netClassSysfsPath, iface, "device")); symlinkErr == nil {
			if slot, slotErr := pciSlotID(devicePath); slotErr == nil {
				candidate.slot = slot
			}
		}
		candidates = append(candidates, candidate)
	}

	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no GNSS device found for EthernetDevice")
	case 1:
		return candidates[0].device, nil
	default:
		matches := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			matches = append(matches, fmt.Sprintf("%s (interface %s, slot %s)", candidate.device, candidate.iface, candidate.slot))
		}
		return "", fmt.Errorf("multiple GNSS devices found for EthernetDevice: %s; specify slot to disambiguate",
			strings.Join(matches, ", "))
	}
}

// GNSSDeviceFromACPIDevice resolves the tty exposed by an ACPI-enumerated
// serial controller. It walks each tty's sysfs ancestry and matches an ACPI
// device name such as INTC10EE:00, rather than relying on the dynamically
// assigned tty name (for example, ttyS2).
func GNSSDeviceFromACPIDevice(hid, uid string) (string, error) {
	hid = strings.TrimSpace(hid)
	uid = strings.TrimSpace(uid)
	if hid == "" {
		return "", fmt.Errorf("invalid ACPI hardware ID: must not be empty")
	}
	return findTTYFromACPIDevice(ttyClassSysfsPath, hid, uid)
}

func findTTYFromACPIDevice(ttyClassPath, hid, uid string) (string, error) {
	selector := hid
	if uid != "" {
		selector += ":" + uid
	}
	glog.Infof("Looking for GNSS tty exposed by ACPI serial device %s", selector)
	entries, err := ReadDir(ttyClassPath)
	if err != nil {
		return "", fmt.Errorf("cannot enumerate tty devices: %w", err)
	}

	var candidates []string
	for _, entry := range entries {
		devicePath := filepath.Join(ttyClassPath, entry.Name(), "device")
		resolvedDevicePath, symlinkErr := evalSymlinks(devicePath)
		if symlinkErr != nil {
			// Virtual tty devices and stale class entries may not have a
			// resolvable device path.
			continue
		}
		if acpiDeviceMatches(resolvedDevicePath, hid, uid) {
			candidates = append(candidates, filepath.Join("/dev", entry.Name()))
		}
	}

	sort.Strings(candidates)
	switch len(candidates) {
	case 0:
		return "", fmt.Errorf("no tty device found for ACPI serial device %s", selector)
	case 1:
		glog.Infof("Detected GNSS device %s", candidates[0])
		return candidates[0], nil
	default:
		return "", fmt.Errorf("multiple tty devices found for ACPI serial device %s: %s",
			selector, strings.Join(candidates, ", "))
	}
}

// acpiDeviceMatches walks from a tty's sysfs device to its parents, looking
// for an ACPI device directory named <HID>:<UID>. If uid is empty, any
// instance of the requested HID matches.
func acpiDeviceMatches(devicePath, hid, uid string) bool {
	for path := devicePath; path != "." && path != string(filepath.Separator); path = filepath.Dir(path) {
		name := filepath.Base(path)
		parts := strings.SplitN(name, ":", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], hid) {
			continue
		}
		if uid == "" || strings.EqualFold(parts[1], uid) {
			return true
		}
	}
	return false
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
		resolvedDevicePath, symlinkErr := evalSymlinks(devicePath)
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
		subsystemPath, err := evalSymlinks(filepath.Join(sysfsPath, "subsystem"))
		if err == nil && filepath.Base(subsystemPath) == "usb" {
			matchedPath := filepath.Base(sysfsPath)
			if topologyPath != "" && matchedPath != topologyPath {
				continue
			}
			actualVendor, vendorErr := readFile(filepath.Join(sysfsPath, "idVendor"))
			actualProduct, productErr := readFile(filepath.Join(sysfsPath, "idProduct"))
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
	return normalizePCIID(id)
}

func normalizePCIAddress(address string) (string, error) {
	address = strings.TrimSpace(address)
	if filepath.Base(address) != address || address == "." || address == ".." {
		return "", fmt.Errorf("invalid PCI address %q", address)
	}
	if strings.Count(address, ":") == 1 {
		address = "0000:" + address
	}
	if !pciAddressPattern.MatchString(address) {
		return "", fmt.Errorf("invalid PCI address %q", address)
	}
	return address, nil
}

func normalizePCIID(id string) (string, error) {
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
