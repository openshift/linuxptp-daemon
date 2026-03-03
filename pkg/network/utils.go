package network

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
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

// VPD parsing constants (PCI Local Bus Specification)
const (
	pciVPDIDStringTag        = 0x82
	pciVPDROTag              = 0x90
	pciVPDEndTag             = 0x78
	pciVPDBlockDescriptorLen = 3
	pciVPDKeywordLen         = 2
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
		// glog.Infof("grabbing NIC timestamp capability for %v", dev.Name)
		cmd := exec.Command(ethtoolPath, "-T", dev.Name)
		cmd.Stdout = &out
		err := cmd.Run()
		if err != nil {
			glog.Infof("could not grab NIC timestamp capability for %v: %v", dev.Name, err)
		}

		if !netParseEthtoolTimeStampFeature(&out) {
			glog.Infof("Skipping NIC %v as it does not support HW timestamping", dev.Name)
			continue
		}

		if dev.PCIAddress == nil {
			glog.Warningf("Skipping NIC %v as it does not have a PCI address", dev.Name)
			continue
		}

		// If the physfn doesn't exist this means the interface is not a virtual function so we ca add it to the list
		if _, err = os.Stat(fmt.Sprintf("/sys/bus/pci/devices/%s/physfn", *dev.PCIAddress)); os.IsNotExist(err) {
			nics = append(nics, dev.Name)
		}
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

// GetPCIAddress returns the PCI address for a network device
func GetPCIAddress(deviceName string) (string, error) {
	net, err := ghw.Network()
	if err != nil {
		return "", fmt.Errorf("failed to get network info: %v", err)
	}

	for _, nic := range net.NICs {
		if nic.Name == deviceName {
			if nic.PCIAddress != nil {
				return *nic.PCIAddress, nil
			}
			return "", fmt.Errorf("device %s has no PCI address", deviceName)
		}
	}
	return "", fmt.Errorf("device %s not found", deviceName)
}

// GetNetDevicesFromPCI returns all network interface names associated with a PCI address.
// It checks multiple sysfs paths since availability varies by environment (containers, etc.).
func GetNetDevicesFromPCI(pciAddress string) []string {
	seen := map[string]bool{}
	var names []string

	// Method 1: Try /sys/bus/pci/devices/<pci_addr>/net/
	netDir := fmt.Sprintf("/sys/bus/pci/devices/%s/net", pciAddress)
	if entries, err := os.ReadDir(netDir); err == nil {
		for _, e := range entries {
			if !seen[e.Name()] {
				names = append(names, e.Name())
				seen[e.Name()] = true
			}
		}
	}

	// Method 2: Scan /sys/class/net/*/device symlinks
	if netDevices, err := os.ReadDir("/sys/class/net"); err == nil {
		for _, dev := range netDevices {
			deviceLink := filepath.Join("/sys/class/net", dev.Name(), "device")
			target, linkErr := os.Readlink(deviceLink)
			if linkErr != nil {
				continue
			}
			if strings.HasSuffix(target, pciAddress) && !seen[dev.Name()] {
				names = append(names, dev.Name())
				seen[dev.Name()] = true
			}
		}
	}

	return names
}

// GetVPDInfo reads and parses VPD (Vital Product Data) for a network device
// by streaming the sysfs vpd file exposed by the kernel driver.
func GetVPDInfo(deviceName string) (*VPDInfo, error) {
	vpdPath := fmt.Sprintf("/sys/class/net/%s/device/vpd", deviceName)
	f, err := os.Open(vpdPath)
	if err != nil {
		return nil, fmt.Errorf("could not open VPD for %s: %v", deviceName, err)
	}
	defer f.Close()
	return ParseVPD(f)
}

// GetVPDInfoByPCIPath reads and parses VPD from a PCI device sysfs path
func GetVPDInfoByPCIPath(pciPath string) (*VPDInfo, error) {
	vpdPath := filepath.Join(pciPath, "vpd")
	f, err := os.Open(vpdPath)
	if err != nil {
		return nil, fmt.Errorf("could not open VPD: %v", err)
	}
	defer f.Close()
	return ParseVPD(f)
}

// ResolveNetDeviceNames returns all network interface names for a PCI address,
// with primaryDeviceName listed first. Additional port names/aliases are discovered
// from sysfs and appended in the order found.
func ResolveNetDeviceNames(pciAddress, primaryDeviceName string) []string {
	candidates := []string{primaryDeviceName}
	for _, name := range GetNetDevicesFromPCI(pciAddress) {
		if name != primaryDeviceName {
			candidates = append(candidates, name)
		}
	}
	return candidates
}

// GetVPDInfoByLspci parses VPD from "lspci -vvv -s <pciAddress>" output.
// This works when the sysfs vpd file is not exposed by the driver.
func GetVPDInfoByLspci(pciAddress string) (*VPDInfo, error) {
	out, err := exec.Command("lspci", "-vvv", "-s", pciAddress).Output()
	if err != nil {
		return nil, fmt.Errorf("lspci -vvv -s %s failed: %v", pciAddress, err)
	}
	return parseLspciVPD(string(out)), nil
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

// GetVPDInfoForPCIDevice reads VPD using lspci -vvv (primary) with sysfs as fallback.
// lspci accesses VPD through the PCI config space which works even when the
// sysfs vpd file is not exposed by the driver.
func GetVPDInfoForPCIDevice(pciAddress, primaryDeviceName string) (*VPDInfo, error) {
	// Primary: use lspci -vvv
	vpd, err := GetVPDInfoByLspci(pciAddress)
	if err == nil && vpd.PartNumber != "" {
		return vpd, nil
	}
	if err != nil {
		glog.V(4).Infof("lspci VPD for %s failed: %v", pciAddress, err)
	} else {
		glog.V(4).Infof("lspci VPD for %s returned no data", pciAddress)
	}

	// Fallback: try sysfs vpd file via each network interface name
	candidates := ResolveNetDeviceNames(pciAddress, primaryDeviceName)
	for _, name := range candidates {
		vpdResult, vpdErr := GetVPDInfo(name)
		if vpdErr == nil {
			return vpdResult, nil
		}
		glog.V(4).Infof("Falling back to sysfs VPD for %s failed: %v", name, err)
	}
	return nil, fmt.Errorf("no VPD data found for PCI device %s", pciAddress)
}

// ParseVPD streams and parses binary VPD data from an io.Reader according to
// the PCI Local Bus Specification. It reads tag-by-tag, only consuming as many
// bytes as the VPD structure contains, and stops at the end tag (0x78).
func ParseVPD(r io.Reader) (*VPDInfo, error) {
	vpd := &VPDInfo{}
	header := make([]byte, pciVPDBlockDescriptorLen)

	for {
		if _, err := io.ReadFull(r, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return vpd, fmt.Errorf("reading VPD header: %w", err)
		}

		tag := header[0]
		if tag == pciVPDEndTag {
			break
		}

		lenBlock := int(binary.LittleEndian.Uint16(header[1:pciVPDBlockDescriptorLen]))
		block := make([]byte, lenBlock)
		if _, err := io.ReadFull(r, block); err != nil {
			return vpd, fmt.Errorf("reading VPD block (tag 0x%02x, len %d): %w", tag, lenBlock, err)
		}

		switch tag {
		case pciVPDIDStringTag:
			vpd.IdentifierString = cleanVPDString(string(block))
		case pciVPDROTag:
			for k, v := range parseVPDBlock(block) {
				switch k {
				case "SN":
					vpd.SerialNumber = v
				case "PN":
					vpd.PartNumber = v
				case "MN":
					vpd.ManufacturerID = v
				case "V0":
					vpd.ProductName = v
				case "V1":
					vpd.VendorSpecific1 = v
				case "V2":
					vpd.VendorSpecific2 = v
				}
			}
		}
	}

	return vpd, nil
}

// parseVPDBlock parses a VPD read-only or read-write block
func parseVPDBlock(block []byte) map[string]string {
	rv := map[string]string{}
	lenBlock := len(block)
	offset := 0

	for offset+pciVPDKeywordLen+1 <= lenBlock {
		kw := string(block[offset : offset+pciVPDKeywordLen])
		ln := int(block[offset+pciVPDKeywordLen])

		dataStart := offset + pciVPDKeywordLen + 1
		dataEnd := dataStart + ln

		if dataEnd > lenBlock {
			break
		}

		data := block[dataStart:dataEnd]
		// Extract common fields
		if strings.HasPrefix(kw, "V") || kw == "PN" || kw == "SN" || kw == "MN" {
			rv[kw] = cleanVPDString(string(data))
		}

		offset = dataEnd
	}

	return rv
}

// cleanVPDString removes null bytes and non-printable characters from VPD strings
func cleanVPDString(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 32 && r < 127 {
			return r
		}
		return -1
	}, strings.TrimSpace(s))
}
