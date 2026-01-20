package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
	"github.com/jaypipes/ghw"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// logStructuredHardwareInfo logs detailed hardware information in structured format
func logStructuredHardwareInfo(deviceName string, hwInfo *ptpv1.HardwareInfo) {
	if hwInfo == nil {
		glog.Infof("PTP Device: %s (no hardware info available)", deviceName)
		return
	}

	glog.Infof("PTP Device Hardware Info: %s", deviceName)

	// PCI Information
	if hwInfo.PCIAddress != "" {
		glog.Infof("  PCI Address:        %s", hwInfo.PCIAddress)
	}
	if hwInfo.VendorID != "" {
		glog.Infof("  Vendor ID:          %s", hwInfo.VendorID)
	}
	if hwInfo.DeviceID != "" {
		glog.Infof("  Device ID:          %s", hwInfo.DeviceID)
	}
	if hwInfo.SubsystemVendorID != "" {
		glog.Infof("  Subsystem Vendor:   %s", hwInfo.SubsystemVendorID)
	}
	if hwInfo.SubsystemDeviceID != "" {
		glog.Infof("  Subsystem Device:   %s", hwInfo.SubsystemDeviceID)
	}

	// Firmware and Driver Information
	if hwInfo.FirmwareVersion != "" {
		glog.Infof("  Firmware Version:   %s", hwInfo.FirmwareVersion)
	}
	if hwInfo.DriverVersion != "" {
		glog.Infof("  Driver Version:     %s", hwInfo.DriverVersion)
	}

	// VPD Information
	if hwInfo.VPDPartNumber != "" {
		glog.Infof("  VPD Part Number:    %s", hwInfo.VPDPartNumber)
	}
	if hwInfo.VPDSerialNumber != "" {
		glog.Infof("  VPD Serial Number:  %s", hwInfo.VPDSerialNumber)
	}
	if hwInfo.VPDManufacturerID != "" {
		glog.Infof("  VPD Manufacturer:   %s", hwInfo.VPDManufacturerID)
	}
	if hwInfo.VPDProductName != "" {
		glog.Infof("  VPD Product Name:   %s", hwInfo.VPDProductName)
	}
}

// getHardwareInfo collects detailed hardware information for a network device
func getHardwareInfo(deviceName string) (*ptpv1.HardwareInfo, error) {
	net, err := ghw.Network()
	if err != nil {
		return nil, fmt.Errorf("failed to get network info for device %s: %v", deviceName, err)
	}

	// Find the NIC in ghw data
	var targetNIC *ghw.NIC
	for _, nic := range net.NICs {
		if nic.Name == deviceName {
			targetNIC = nic
			break
		}
	}

	if targetNIC == nil {
		return nil, fmt.Errorf("device %s not found in hardware inventory", deviceName)
	}

	hwInfo := &ptpv1.HardwareInfo{}

	// Get PCI information
	if targetNIC.PCIAddress == nil {
		return nil, errors.New("no PCI address found for the target NIC")
	}
	hwInfo.PCIAddress = *targetNIC.PCIAddress
	pciPath := fmt.Sprintf("/sys/bus/pci/devices/%s", *targetNIC.PCIAddress)

	// Read PCI IDs from sysfs
	hwInfo.VendorID = readSysfsFile(filepath.Join(pciPath, "vendor"))
	hwInfo.DeviceID = readSysfsFile(filepath.Join(pciPath, "device"))
	hwInfo.SubsystemVendorID = readSysfsFile(filepath.Join(pciPath, "subsystem_vendor"))
	hwInfo.SubsystemDeviceID = readSysfsFile(filepath.Join(pciPath, "subsystem_device"))

	// Get driver information
	driverLink, readErr := os.Readlink(filepath.Join(pciPath, "driver"))
	if readErr != nil {
		return nil, fmt.Errorf("failed to get driver for device %s: %v", deviceName, readErr)
	}
	driverName := filepath.Base(driverLink)
	driverModulePath := filepath.Join(pciPath, "driver", "module")

	// Try to get driver version
	if driverVer := readSysfsFile(filepath.Join(driverModulePath, "version")); driverVer != "" {
		hwInfo.DriverVersion = fmt.Sprintf("%s v%s", driverName, driverVer)
	} else {
		hwInfo.DriverVersion = driverName
	}

	// Try to get firmware version (device-specific paths)
	hwInfo.FirmwareVersion = getFirmwareVersion(pciPath, deviceName)

	// Try to get VPD data
	vpdData := readVPDData(pciPath)
	if vpdData != nil {
		hwInfo.VPDPartNumber = vpdData.PartNumber
		hwInfo.VPDSerialNumber = vpdData.SerialNumber
		hwInfo.VPDManufacturerID = vpdData.ManufacturerID
		hwInfo.VPDProductName = vpdData.ProductName
	} else {
		glog.V(2).Infof("No VPD data found for device %s", deviceName)
	}

	return hwInfo, nil
}

// readSysfsFile reads a single-line value from sysfs and trims whitespace
func readSysfsFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		glog.V(2).Infof("could not read sysfs file %s: %v", path, err)
		return ""
	}
	return strings.TrimSpace(string(data))
}

// getFirmwareVersion attempts to read firmware version from various device-specific locations
func getFirmwareVersion(pciPath, deviceName string) string {
	// Try common firmware version paths
	fwPaths := []string{
		filepath.Join(pciPath, "firmware_version"),
		filepath.Join("/sys/class/net", deviceName, "device", "fw_version"),
		filepath.Join(pciPath, "fw_ver"),
	}

	for _, path := range fwPaths {
		if fwVer := readSysfsFile(path); fwVer != "" {
			return fwVer
		}
	}

	return ""
}

// VPDData holds Vital Product Data parsed from device VPD
type VPDData struct {
	PartNumber     string
	SerialNumber   string
	ManufacturerID string
	ProductName    string
}

// readVPDData attempts to read and parse VPD data from device
func readVPDData(pciPath string) *VPDData {
	vpdPath := filepath.Join(pciPath, "vpd")
	data, err := os.ReadFile(vpdPath)
	if err != nil {
		return nil
	}

	// VPD data is in a complex binary format (PCI Local Bus Specification)
	// This is a simplified parser for common read-only fields
	vpd := &VPDData{}

	// Look for VPD-R (read-only) section tag 0x90
	// Common tags: PN (Part Number), SN (Serial Number), MN (Manufacturer), V0-VZ (Vendor specific)
	vpd.PartNumber = extractVPDField(data, "PN")
	vpd.SerialNumber = extractVPDField(data, "SN")
	vpd.ManufacturerID = extractVPDField(data, "MN")
	vpd.ProductName = extractVPDField(data, "V0") // V0 often contains product name

	// Return nil if no fields were found
	if vpd.PartNumber == "" && vpd.SerialNumber == "" && vpd.ManufacturerID == "" && vpd.ProductName == "" {
		return nil
	}

	return vpd
}

// extractVPDField extracts a specific field from VPD binary data
func extractVPDField(data []byte, keyword string) string {
	// VPD format: each field has 2-byte keyword, 1-byte length, then data
	keywordBytes := []byte(keyword)
	keywordLen := len(keywordBytes)
	if len(data) < keywordLen+1 {
		return ""
	}

	for i := 0; i <= len(data)-(keywordLen+1); i++ {
		// Look for keyword match
		if !bytes.Equal(data[i:i+keywordLen], keywordBytes) {
			continue
		}

		// Next byte is length
		length := int(data[i+keywordLen])
		start := i + keywordLen + 1
		end := start + length

		if end > len(data) {
			continue
		}

		// Extract and clean the value
		value := strings.TrimSpace(string(data[start:end]))
		// Remove null bytes and non-printable characters
		value = strings.Map(func(r rune) rune {
			if r >= 32 && r < 127 {
				return r
			}
			return -1
		}, value)
		if value != "" {
			return value
		}
	}

	return ""
}

// logDeviceChanges compares old and new device lists and logs changes
func logDeviceChanges(oldDevices []ptpv1.PtpDevice, newDevices []ptpv1.PtpDevice) {
	oldDeviceMap := make(map[string]bool, len(oldDevices))
	for _, dev := range oldDevices {
		oldDeviceMap[dev.Name] = true
	}

	addedDevices := []string{}
	for _, dev := range newDevices {
		if !oldDeviceMap[dev.Name] {
			addedDevices = append(addedDevices, dev.Name)
		} else {
			// Device found, remove from map to track removals
			delete(oldDeviceMap, dev.Name)
		}
	}

	// Remaining devices in the map are the removed ones
	removedDevices := make([]string, 0, len(oldDeviceMap))
	for name := range oldDeviceMap {
		removedDevices = append(removedDevices, name)
	}

	// Log changes
	if len(addedDevices) > 0 {
		glog.Infof("PTP devices added: %v", addedDevices)
	}
	if len(removedDevices) > 0 {
		glog.Warningf("PTP devices removed: %v", removedDevices)
	}
	if len(addedDevices) == 0 && len(removedDevices) == 0 && len(newDevices) > 0 {
		glog.V(2).Infof("No PTP device changes detected (%d devices)", len(newDevices))
	}
}
