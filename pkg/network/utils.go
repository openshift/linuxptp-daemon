package network

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
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

func ethtoolInstalled() bool {
	_, err := exec.LookPath("ethtool")
	return err == nil
}

func netParseEthtoolTimeStampFeature(cmdOut *bytes.Buffer) bool {
	var hardRxEnabled bool
	var hardTxEnabled bool
	var hardRawEnabled bool

	glog.V(2).Infof("cmd output for %v", cmdOut)
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

// DiscoverPTPDevices returns a list of NICs that have PTP timestamping capabilities
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
		glog.Infof("grabbing NIC timestamp capability for %v", dev.Name)
		cmd := exec.Command(ethtoolPath, "-T", dev.Name)
		cmd.Stdout = &out
		err2 := cmd.Run()
		if err2 != nil {
			glog.Infof("could not grab NIC timestamp capability for %v: %v", dev.Name, err2)
		}

		if !netParseEthtoolTimeStampFeature(&out) {
			continue
		}

		link, err2 := os.Readlink(fmt.Sprintf("/sys/class/net/%s", dev.Name))
		if err2 != nil {
			glog.Infof("could not grab NIC PCI address for %v: %v", dev.Name, err2)
			continue
		}

		var PCIAddr string
		pathSegments := strings.Split(link, "/")
		if len(pathSegments)-3 <= 0 {
			glog.Infof("unexpected sysfs address for %v: %v", dev.Name, out.String())
			continue
		}

		// sysfs address for N3000 looks like: ../../devices/pci0000:85/0000:85:00.0/0000:86:00.0/0000:87:10.0/0000:8a:00.1/net/eth1
		// sysfs address looks like: /sys/devices/pci0000:17/0000:17:02.0/0000:19:00.5/net/eno1
		PCIAddr = pathSegments[len(pathSegments)-3]

		if _, err3 := os.Stat(fmt.Sprintf("/sys/bus/pci/devices/%s", PCIAddr)); os.IsNotExist(err3) {
			glog.Infof("unexpected device address for device name %s PCI %s: %v", dev.Name, PCIAddr, err3)
			continue
		}

		// If the physfn doesn't exist this means the interface is not a virtual function so we ca add it to the list
		if _, err3 := os.Stat(fmt.Sprintf("/sys/bus/pci/devices/%s/physfn", PCIAddr)); os.IsNotExist(err3) {
			nics = append(nics, dev.Name)
		}
	}
	return nics, nil
}

// GetPhcId returns the PTP clock device ID for a given NIC
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
		return 0, fmt.Errorf("discoverDevices(): ethtool not installed. Cannot grab NIC capabilities")
	}

	// Command to get PTP clock info
	cmd := exec.Command("ethtool", "-T", iface)

	// Execute the command and capture output
	out, err := cmd.CombinedOutput()
	if err != nil {
		return -1, fmt.Errorf("failed to run ethtool: %w", err)
	}

	// Regex to extract clock index
	re := regexp.MustCompile(`PTP Hardware Clock: (\d+)`)
	match := re.FindSubmatch(out)
	if match == nil {
		return -1, fmt.Errorf("no PTP hardware clock found on %s", iface)
	}
	// Convert captured index string to integer
	var clockIndex int
	_, err = fmt.Sscanln(string(match[1]), &clockIndex)
	if err != nil {
		return 0, err
	}
	return clockIndex, nil
}
