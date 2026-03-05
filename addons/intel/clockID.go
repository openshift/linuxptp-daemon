package intel

import (
	"encoding/binary"
	"fmt"
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

func getPCIClockID(device string) uint64 {
	b, err := filesystem.ReadFile(fmt.Sprintf("/sys/class/net/%s/device/config", device))
	if err != nil {
		glog.Error(err)
		return 0
	}
	// Extended capability space starts right on PCI_CFG_SPACE
	var offset uint16 = pciConfigSpaceSize
	var id uint16
	for {
		if len(b) < int(offset) {
			glog.Errorf("PCI Config space out of bounds (Offset %d >= %d)", offset, len(b))
			return 0
		}
		id = binary.LittleEndian.Uint16(b[offset:])
		if id != pciExtendedCapabilityDsnID {
			if id == 0 {
				glog.Errorf("can't find DSN for device %s (%d)", device, id)
				return 0
			}
			offset = binary.LittleEndian.Uint16(b[offset+pciExtendedCapabilityNextOffset:]) >> pciExtendedCapabilityOffsetShift
			continue
		}
		break
	}
	return binary.LittleEndian.Uint64(b[offset+pciExtendedCapabilityDataOffset:])
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
