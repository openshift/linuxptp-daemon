package network

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/golang/glog"
	"github.com/jaypipes/ghw"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

// LogStructuredHardwareInfo logs detailed hardware information in structured format
func LogStructuredHardwareInfo(deviceName string, hwInfo *ptpv1.HardwareInfo) {
	if hwInfo == nil {
		glog.Infof("PTP Device: %s (no hardware info available)", deviceName)
		return
	}

	glog.Infof("PTP Device Hardware Info: %s", deviceName)

	// PCI Information
	if hwInfo.PCIAddress != "" {
		glog.Infof("  PCI Address:        %s", hwInfo.PCIAddress)
	}

	// Firmware and Driver Information
	if hwInfo.FirmwareVersion != "" {
		glog.Infof("  Firmware Version:   %s", hwInfo.FirmwareVersion)
	}
	if hwInfo.DriverVersion != "" {
		glog.Infof("  Driver Version:     %s", hwInfo.DriverVersion)
	}

	// Link Status Information
	if hwInfo.LinkStatus != "" {
		glog.Infof("  Link Status:        %s", hwInfo.LinkStatus)
	}
	if hwInfo.LinkSpeed != "" {
		glog.Infof("  Link Speed:         %s", hwInfo.LinkSpeed)
	}
	if hwInfo.FEC != "" {
		glog.Infof("  FEC:                %s", hwInfo.FEC)
	}

	// VPD Information
	if hwInfo.VPDIdentifierString != "" {
		glog.Infof("  VPD Identifier:     %s", hwInfo.VPDIdentifierString)
	}
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
	if hwInfo.VPDVendorSpecific1 != "" {
		glog.Infof("  VPD Vendor V1:      %s", hwInfo.VPDVendorSpecific1)
	}
	if hwInfo.VPDVendorSpecific2 != "" {
		glog.Infof("  VPD Vendor V2:      %s", hwInfo.VPDVendorSpecific2)
	}
}

// GetHardwareInfo collects detailed hardware information for a network device
func GetHardwareInfo(deviceName string) (*ptpv1.HardwareInfo, error) {
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

	// Get driver name, driver version, and firmware version via "ethtool -i"
	ethtoolData, err := GetEthtoolInfo(deviceName)
	if err != nil {
		glog.V(2).Infof("Could not get ethtool info for %s: %v", deviceName, err)
		// Falling back to reading the driver name from lspci or sysfs
		if driver := GetDriverByLspci(*targetNIC.PCIAddress); driver != "" {
			hwInfo.DriverVersion = driver
		} else if driverLink, readErr := os.Readlink(filepath.Join(pciPath, "driver")); readErr == nil {
			hwInfo.DriverVersion = filepath.Base(driverLink)
		}
	} else {
		if ethtoolData.DriverVersion != "" {
			hwInfo.DriverVersion = fmt.Sprintf("%s v%s", ethtoolData.Driver, ethtoolData.DriverVersion)
		} else if ethtoolData.Driver != "" {
			hwInfo.DriverVersion = ethtoolData.Driver
		}
		hwInfo.FirmwareVersion = ethtoolData.FirmwareVersion
	}

	// Get link status, speed, and FEC via ethtool
	linkInfo, err := GetLinkInfo(deviceName)
	if err != nil {
		glog.V(2).Infof("Could not get link info for %s: %v", deviceName, err)
	} else {
		if linkInfo.LinkDetected {
			hwInfo.LinkStatus = "up"
		} else {
			hwInfo.LinkStatus = "down"
		}
		hwInfo.LinkSpeed = linkInfo.Speed
		hwInfo.FEC = linkInfo.FEC
	}

	// Get VPD data via lspci -vvv (primary), falling back to sysfs vpd file
	vpdData, err := GetVPDInfoForPCIDevice(hwInfo.PCIAddress, deviceName)
	if err != nil {
		glog.V(4).Infof("No VPD data found for device %s: %v", deviceName, err)
	} else {
		hwInfo.VPDIdentifierString = vpdData.IdentifierString
		hwInfo.VPDPartNumber = vpdData.PartNumber
		hwInfo.VPDSerialNumber = vpdData.SerialNumber
		hwInfo.VPDManufacturerID = vpdData.ManufacturerID
		hwInfo.VPDProductName = vpdData.ProductName
		hwInfo.VPDVendorSpecific1 = vpdData.VendorSpecific1
		hwInfo.VPDVendorSpecific2 = vpdData.VendorSpecific2
	}

	return hwInfo, nil
}

// LogDeviceChanges compares old and new device lists and logs changes
func LogDeviceChanges(oldDevices []ptpv1.PtpDevice, newDevices []ptpv1.PtpDevice) {
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
