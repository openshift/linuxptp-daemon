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
