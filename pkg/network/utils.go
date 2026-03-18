package network

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/golang/glog"
	"github.com/jaypipes/ghw"
)

const (
	_ETHTOOL_HARDWARE_RECEIVE_CAP   = "hardware-receive"
	_ETHTOOL_HARDWARE_TRANSMIT_CAP  = "hardware-transmit"
	_ETHTOOL_HARDWARE_RAW_CLOCK_CAP = "hardware-raw-clock"
)

// EthtoolInfo holds driver and firmware information from ethtool -i
type EthtoolInfo struct {
	Driver          string
	DriverVersion   string
	FirmwareVersion string
	BusInfo         string
	ExpansionRom    string
}

// LinkInfo holds link status, speed, and FEC information
type LinkInfo struct {
	LinkDetected bool
	Speed        string // e.g., "25000Mb/s" or "Unknown!"
	Duplex       string // e.g., "Full"
	FEC          string // e.g., "RS", "BaseR", "Off", "None"
}

// VPDInfo holds Vital Product Data parsed from device VPD
type VPDInfo struct {
	IdentifierString string
	PartNumber       string
	SerialNumber     string
	ManufacturerID   string
	VendorSpecific1  string // V1 field - often contains product info
	VendorSpecific2  string // V2 field
	ProductName      string // V0 field
}

func ethtoolInstalled() bool {
	_, err := exec.LookPath("ethtool")
	return err == nil
}

func netParseEthtoolTimeStampFeature(cmdOut *bytes.Buffer) bool {
	var hardRxEnabled bool
	var hardTxEnabled bool
	var hardRawEnabled bool

	// glog.V(2).Infof("cmd output for %v", cmdOut)
	scanner := bufio.NewScanner(cmdOut)
	for scanner.Scan() {
		line := strings.TrimPrefix(scanner.Text(), "\t")
		parts := strings.Fields(line)
		if parts[0] == _ETHTOOL_HARDWARE_RECEIVE_CAP {
			hardRxEnabled = true
		}
		if parts[0] == _ETHTOOL_HARDWARE_TRANSMIT_CAP {
			hardTxEnabled = true
		}
		if parts[0] == _ETHTOOL_HARDWARE_RAW_CLOCK_CAP {
			hardRawEnabled = true
		}
	}
	return hardRxEnabled && hardTxEnabled && hardRawEnabled
}

func DiscoverPTPDevices() ([]string, error) {
	var out bytes.Buffer
	nics := make([]string, 0)

	if !ethtoolInstalled() {
		return nics, fmt.Errorf("discoverDevices(): ethtool not installed. Cannot grab NIC capabilities")
	}

	ethtoolPath, _ := exec.LookPath("ethtool")

	net, err := ghw.Network()
	if err != nil {
		return nics, fmt.Errorf("discoverDevices(): error getting network info: %v", err)
	}

	for _, dev := range net.NICs {
		if dev.PCIAddress == nil {
			continue
		}

		if _, err = os.Stat(fmt.Sprintf("/sys/bus/pci/devices/%s/physfn", *dev.PCIAddress)); err == nil {
			continue
		}

		cmd := exec.Command(ethtoolPath, "-T", dev.Name)
		cmd.Stdout = &out
		err := cmd.Run()
		if err != nil {
			glog.V(2).Infof("could not grab NIC timestamp capability for %v: %v", dev.Name, err)
			continue
		}

		if !netParseEthtoolTimeStampFeature(&out) {
			glog.V(2).Infof("Skipping NIC %v: no HW timestamping support", dev.Name)
			continue
		}

		nics = append(nics, dev.Name)
	}
	return nics, nil
}

func GetPhcId(iface string) string {
	var err error
	var id int
	if id, err = getPTPClockIndex(iface); err != nil {
		glog.Error(err.Error())
	} else {
		return fmt.Sprintf("/dev/ptp%d", id)
	}
	return ""
}

func getPTPClockIndex(iface string) (int, error) {
	if !ethtoolInstalled() {
		return 0, fmt.Errorf("discoverDevices(): ethtool not installed")
	}
	out, err := exec.Command("ethtool", "-T", iface).CombinedOutput()
	if err != nil {
		return -1, fmt.Errorf("failed to run ethtool: %w", err)
	}

	// Try classic format
	if m := regexp.MustCompile(`PTP Hardware Clock:\s*(\d+)`).FindSubmatch(out); m != nil {
		var idx int
		_, err = fmt.Sscan(string(m[1]), &idx)
		return idx, err
	}

	// Try provider index format (seen in some containers)
	if m := regexp.MustCompile(`Hardware timestamp provider index:\s*(\d+)`).FindSubmatch(out); m != nil {
		var idx int
		_, err = fmt.Sscan(string(m[1]), &idx)
		return idx, err
	}

	// Sysfs fallback
	matches, err := filepath.Glob(fmt.Sprintf("/sys/class/net/%s/ptp/ptp*", iface))
	if err == nil && len(matches) > 0 {
		base := filepath.Base(matches[0]) // e.g., "ptp0"
		if strings.HasPrefix(base, "ptp") {
			var idx int
			_, err = fmt.Sscan(strings.TrimPrefix(base, "ptp"), &idx)
			return idx, err
		}
	}

	return -1, fmt.Errorf("no PTP clock index found for %s", iface)
}

// GetEthtoolInfo uses ethtool -i to get driver and firmware information
// This is more reliable than sysfs paths which vary by device type
func GetEthtoolInfo(deviceName string) (*EthtoolInfo, error) {
	if !ethtoolInstalled() {
		return nil, fmt.Errorf("ethtool not installed")
	}

	cmd := exec.Command("ethtool", "-i", deviceName)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ethtool -i %s failed: %v", deviceName, err)
	}

	info := &EthtoolInfo{}
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "driver":
			info.Driver = value
		case "version":
			info.DriverVersion = value
		case "firmware-version":
			info.FirmwareVersion = value
		case "bus-info":
			info.BusInfo = value
		case "expansion-rom-version":
			info.ExpansionRom = value
		}
	}
	return info, nil
}

// GetDriverByLspci extracts the kernel driver name from "lspci -v -s <pciAddress>".
// Returns empty string if lspci is unavailable or no driver is reported.
func GetDriverByLspci(pciAddress string) string {
	out, err := exec.Command("lspci", "-v", "-s", pciAddress).Output()
	if err != nil {
		return ""
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "Kernel driver in use:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Kernel driver in use:"))
		}
	}
	return ""
}

// GetLinkInfo uses ethtool to get link status, speed, and FEC information
func GetLinkInfo(deviceName string) (*LinkInfo, error) {
	if !ethtoolInstalled() {
		return nil, fmt.Errorf("ethtool not installed")
	}

	info := &LinkInfo{}

	// Get link status and speed using: ethtool <deviceName>
	cmd := exec.Command("ethtool", deviceName)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ethtool %s failed: %v", deviceName, err)
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "Speed:") {
			speed := strings.TrimSpace(strings.TrimPrefix(line, "Speed:"))
			if speed != "Unknown!" {
				info.Speed = speed
			}
		} else if strings.HasPrefix(line, "Duplex:") {
			info.Duplex = strings.TrimSpace(strings.TrimPrefix(line, "Duplex:"))
		} else if strings.HasPrefix(line, "Link detected:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "Link detected:"))
			info.LinkDetected = (value == "yes")
		}
	}

	// Get FEC using: ethtool --show-fec <deviceName>
	fecCmd := exec.Command("ethtool", "--show-fec", deviceName)
	fecOutput, err := fecCmd.Output()
	if err != nil {
		// FEC query may not be supported on all devices - not an error
		glog.V(4).Infof("ethtool --show-fec %s not supported: %v", deviceName, err)
	} else {
		fecLines := strings.Split(string(fecOutput), "\n")
		for _, line := range fecLines {
			line = strings.TrimSpace(line)
			// Look for "Active FEC encoding:" line
			if strings.HasPrefix(line, "Active FEC encoding:") {
				info.FEC = strings.TrimSpace(strings.TrimPrefix(line, "Active FEC encoding:"))
				break
			}
			// Some drivers use "Configured FEC encodings:" when no active link
			if strings.HasPrefix(line, "Configured FEC encodings:") && info.FEC == "" {
				info.FEC = strings.TrimSpace(strings.TrimPrefix(line, "Configured FEC encodings:"))
			}
		}
	}

	return info, nil
}

// GetVPDInfo reads VPD for a PCI device, trying lspci first then sysfs as fallback.
func GetVPDInfo(pciAddress string) (*VPDInfo, error) {
	// Try lspci (works when container has full PCI config space access)
	out, err := exec.Command("lspci", "-vvv", "-s", pciAddress).Output()
	if err == nil && strings.Contains(string(out), "Vital Product Data") {
		vpd := parseLspciVPD(string(out))
		if vpd.PartNumber != "" || vpd.IdentifierString != "" || vpd.SerialNumber != "" {
			return vpd, nil
		}
	}

	// Fallback: read binary VPD from sysfs (kernel handles PCI config space access)
	vpdPath := fmt.Sprintf("/sys/bus/pci/devices/%s/vpd", pciAddress)
	f, openErr := os.Open(vpdPath)
	if openErr != nil {
		return &VPDInfo{}, fmt.Errorf("no VPD available for %s", pciAddress)
	}
	defer f.Close()
	vpd, parseErr := parseSysfsVPD(f)
	if parseErr != nil {
		return &VPDInfo{}, fmt.Errorf("failed to parse sysfs VPD for %s: %v", pciAddress, parseErr)
	}
	return vpd, nil
}

// parseSysfsVPD reads and parses binary VPD data from a sysfs vpd file.
// The kernel exposes VPD as a binary blob following the PCI Local Bus Specification.
func parseSysfsVPD(f *os.File) (*VPDInfo, error) {
	data, err := os.ReadFile(f.Name())
	if err != nil {
		return nil, fmt.Errorf("reading VPD data: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("VPD file is empty")
	}
	vpd := &VPDInfo{}
	offset := 0
	for offset < len(data) {
		tag := data[offset]
		if tag == 0x78 { // end tag
			break
		}
		if offset+3 > len(data) {
			break
		}
		blockLen := int(data[offset+1]) | int(data[offset+2])<<8
		offset += 3
		if offset+blockLen > len(data) {
			break
		}
		block := data[offset : offset+blockLen]
		switch tag {
		case 0x82: // identifier string
			vpd.IdentifierString = cleanVPDBytes(block)
		case 0x90: // VPD-R (read-only)
			parseVPDKeywords(block, vpd)
		}
		offset += blockLen
	}
	return vpd, nil
}

func parseVPDKeywords(block []byte, vpd *VPDInfo) {
	offset := 0
	for offset+3 <= len(block) {
		kw := string(block[offset : offset+2])
		ln := int(block[offset+2])
		offset += 3
		if offset+ln > len(block) {
			break
		}
		val := cleanVPDBytes(block[offset : offset+ln])
		switch kw {
		case "PN":
			vpd.PartNumber = val
		case "SN":
			vpd.SerialNumber = val
		case "MN":
			vpd.ManufacturerID = val
		case "V0":
			vpd.ProductName = val
		case "V1":
			vpd.VendorSpecific1 = val
		case "V2":
			vpd.VendorSpecific2 = val
		}
		offset += ln
	}
}

func cleanVPDBytes(b []byte) string {
	return strings.TrimRight(strings.Map(func(r rune) rune {
		if r >= 32 && r < 127 {
			return r
		}
		return -1
	}, string(b)), " ")
}

// parseLspciVPD extracts VPD fields from lspci -vvv output.
func parseLspciVPD(output string) *VPDInfo {
	vpd := &VPDInfo{}
	inVPD := false
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "Vital Product Data") {
			inVPD = true
			continue
		}
		if inVPD {
			// VPD section ends at next non-indented line or "End" marker
			if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "        ") {
				break
			}
			if strings.HasPrefix(trimmed, "End") {
				break
			}
			if strings.HasPrefix(trimmed, "Product Name:") {
				vpd.IdentifierString = strings.TrimSpace(strings.TrimPrefix(trimmed, "Product Name:"))
			}
			if strings.HasPrefix(trimmed, "[PN]") {
				vpd.PartNumber = extractLspciField(trimmed)
			}
			if strings.HasPrefix(trimmed, "[SN]") {
				vpd.SerialNumber = extractLspciField(trimmed)
			}
			if strings.HasPrefix(trimmed, "[MN]") {
				vpd.ManufacturerID = extractLspciField(trimmed)
			}
			if strings.HasPrefix(trimmed, "[V0]") {
				vpd.ProductName = extractLspciField(trimmed)
			}
			if strings.HasPrefix(trimmed, "[V1]") {
				vpd.VendorSpecific1 = extractLspciField(trimmed)
			}
			if strings.HasPrefix(trimmed, "[V2]") {
				vpd.VendorSpecific2 = extractLspciField(trimmed)
			}
		}
	}
	return vpd
}

// extractLspciField extracts the value after the colon in a lspci VPD field line.
// e.g., "[PN] Part number: N66177-006" -> "N66177-006"
func extractLspciField(line string) string {
	if idx := strings.Index(line, ":"); idx != -1 && idx+1 < len(line) {
		return strings.TrimSpace(line[idx+1:])
	}
	return ""
}
