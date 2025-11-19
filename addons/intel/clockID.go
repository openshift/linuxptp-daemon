package intel

import (
	"encoding/binary"
	"fmt"

	"github.com/golang/glog"
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
