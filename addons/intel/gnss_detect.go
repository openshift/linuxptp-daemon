package intel

import (
	"fmt"
	"strings"

	"github.com/bigkevmcd/go-configparser"
	"github.com/golang/glog"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
)

const (
	nmeaSerialPortKey       = "ts2phc.nmea_serialport"
	ts2phcMasterKey         = "ts2phc.master"
	gnssDeviceSysfsTemplate = "/sys/class/net/%s/device/gnss"
)

var knownNonInterfaceSections = map[string]bool{
	"global": true,
	"nmea":   true,
}

// findLeadingInterface parses a ts2phcConf string to determine if auto-detection
// of the GNSS serial port is needed, and if so, returns the leading interface name.
//
// Returns the interface name and true if auto-detection should proceed:
//   - [nmea] section must be present (T-GM profile)
//   - ts2phc.nmea_serialport must NOT be already set in [global]
//   - At least one interface section must exist
//
// The leading interface is the one that does NOT have "ts2phc.master 0".
func findLeadingInterface(ts2phcConf string) (string, bool) {
	conf, err := configparser.ParseReaderWithOptions(
		strings.NewReader(ts2phcConf),
		configparser.Delimiters(" "),
	)
	if err != nil {
		glog.Warningf("failed to parse ts2phcConf: %v", err)
		return "", false
	}

	if !conf.HasSection("nmea") {
		return "", false
	}

	if serialPort, _ := conf.Get("global", nmeaSerialPortKey); serialPort != "" {
		glog.Infof("ts2phc.nmea_serialport already set to %q, skipping auto-detection", serialPort)
		return "", false
	}

	var candidates []string
	for _, section := range conf.Sections() {
		if knownNonInterfaceSections[section] {
			continue
		}
		val, _ := conf.Get(section, ts2phcMasterKey)
		if val != "0" {
			candidates = append(candidates, section)
		}
	}

	if len(candidates) == 0 {
		glog.Warning("T-GM profile detected but no leading interface found (all interfaces have ts2phc.master 0)")
		return "", false
	}
	if len(candidates) > 1 {
		glog.Warningf("multiple interfaces without ts2phc.master 0: %v; using %s", candidates, candidates[0])
	}
	return candidates[0], true
}

// gnssDeviceFromInterface resolves the GNSS device path for a given network interface
// by reading the sysfs directory /sys/class/net/<iface>/device/gnss/.
func gnssDeviceFromInterface(iface string) (string, error) {
	gnssDir := fmt.Sprintf(gnssDeviceSysfsTemplate, iface)
	entries, err := filesystem.ReadDir(gnssDir)
	if err != nil {
		return "", fmt.Errorf("no GNSS device found for interface %s: %w", iface, err)
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("GNSS sysfs directory %s is empty", gnssDir)
	}
	if len(entries) > 1 {
		glog.Warningf("multiple GNSS devices found for %s, using %s", iface, entries[0].Name())
	}
	return fmt.Sprintf("/dev/%s", entries[0].Name()), nil
}

// autoDetectGNSSSerialPort attempts to auto-detect the GNSS serial port
// from the ts2phcConf and patch it into the node profile if needed.
func autoDetectGNSSSerialPort(nodeProfile *ptpv1.PtpProfile) {
	if nodeProfile.Ts2PhcConf == nil {
		return
	}
	leadingIface, found := findLeadingInterface(*nodeProfile.Ts2PhcConf)
	if !found {
		return
	}
	gnssPath, err := gnssDeviceFromInterface(leadingIface)
	if err != nil {
		glog.Warningf("could not auto-detect GNSS device for %s: %v", leadingIface, err)
		return
	}
	glog.Infof("auto-detected GNSS serial port %s from interface %s", gnssPath, leadingIface)

	// Insert into the raw config string to preserve original formatting.
	// This mirrors how ExtendGlobalSection in config.go modifies the parsed config.
	setting := fmt.Sprintf("%s %s", nmeaSerialPortKey, gnssPath)
	lines := strings.Split(*nodeProfile.Ts2PhcConf, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "[global]" {
			result := make([]string, 0, len(lines)+1)
			result = append(result, lines[:i+1]...)
			result = append(result, setting)
			result = append(result, lines[i+1:]...)
			*nodeProfile.Ts2PhcConf = strings.Join(result, "\n")
			return
		}
	}
}
