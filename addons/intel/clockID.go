package intel

import (
	"encoding/binary"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/golang/glog"
	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
)

const (
	pciConfigSpaceSize               = 256
	pciExtendedCapabilityDsnID       = 3
	pciExtendedCapabilityNextOffset  = 2
	pciExtendedCapabilityOffsetShift = 4
	pciExtendedCapabilityDataOffset  = 4
)

// getClockID returns the DPLL clock ID for a network device.
func getClockID(device string) uint64 {
	clockID := getPCIClockID(device)
	if clockID != 0 {
		return clockID
	}
	return getDevlinkClockID(device)
}

func getPCIClockID(device string) uint64 {
	b, err := filesystem.ReadFile(fmt.Sprintf("/sys/class/net/%s/device/config", device))
	if err != nil {
		glog.Error(err)
		return 0
	}
	var offset uint16 = pciConfigSpaceSize
	var id uint16
	for {
		// TODO: Add test for == case
		if len(b) <= int(offset) {
			glog.Errorf("PCI config space too short (%d bytes) for device %s", len(b), device)
			return 0
		}
		id = binary.LittleEndian.Uint16(b[offset:])
		if id != pciExtendedCapabilityDsnID {
			if id == 0 {
				glog.Errorf("DSN capability not found for device %s", device)
				return 0
			}
			offset = binary.LittleEndian.Uint16(b[offset+pciExtendedCapabilityNextOffset:]) >> pciExtendedCapabilityOffsetShift
			continue
		}
		break
	}
	return binary.LittleEndian.Uint64(b[offset+pciExtendedCapabilityDataOffset:])
}

func getDevlinkClockID(device string) uint64 {
	devicePath, err := filesystem.ReadLink(fmt.Sprintf("/sys/class/net/%s/device", device))
	if err != nil {
		glog.Errorf("failed to resolve PCI address for %s: %v", device, err)
		return 0
	}
	pciAddr := filepath.Base(devicePath)

	out, err := exec.Command("devlink", "dev", "info", "pci/"+pciAddr).Output()
	if err != nil {
		glog.Errorf("getDevlinkClockID: devlink failed for %s (pci/%s): %v", device, pciAddr, err)
		return 0
	}

	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || fields[0] != "serial_number" {
			continue
		}
		var clockID uint64
		// "50-7c-6f-ff-ff-1f-b5-80" -> "507c6fffff1fb580" -> 0x507c6fffff1fb580
		clockID, err = strconv.ParseUint(strings.ReplaceAll(fields[1], "-", ""), 16, 64)
		if err != nil {
			glog.Errorf("getDevlinkClockID: failed to parse serial '%s': %v", fields[1], err)
			return 0
		}
		return clockID
	}
	glog.Errorf("getDevlinkClockID: serial_number not found in devlink output for %s (pci/%s)", device, pciAddr)
	return 0
}

// Using a named anonymous function to allow mocking
var getAllDpllDevices = func() ([]*dpll.DoDeviceGetReply, error) {
	conn, err := dpll.Dial(nil)
	if err != nil {
		return nil, err
	}
	//nolint:errcheck
	defer conn.Close()
	return conn.DumpDeviceGet()
}

// getClockIDByModule returns ClockID for a given DPLL module name, preferring PPS type if present
func getClockIDByModule(module string) (uint64, error) {
	devices, err := getAllDpllDevices()
	if err != nil {
		return 0, err
	}
	var anyID uint64
	for _, d := range devices {
		if strings.EqualFold(d.ModuleName, module) {
			if d.Type == 1 { // PPS
				return d.ClockID, nil
			}
			anyID = d.ClockID
		}
	}
	if anyID != 0 {
		return anyID, nil
	}
	return 0, fmt.Errorf("module %s DPLL not found", module)
}
