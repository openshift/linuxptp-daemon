package intel

import (
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func generatePCIDataForClockID(id uint64) []byte {
	return generateTestPCIDataForClockID(id, 0)
}

func generateTestPCIDataForClockID(id uint64, extraSections int) []byte {
	result := make([]byte, pciConfigSpaceSize)
	nextSectionOffset := pciConfigSpaceSize
	for i := range extraSections {
		id := i + pciExtendedCapabilityDsnID + 1 // Anything != pciExtendedCapabilityDsnID
		sectionSize := (1 << pciExtendedCapabilityOffsetShift) * (i + 1)
		dataSize := sectionSize - pciExtendedCapabilityDataOffset
		nextSectionOffset = nextSectionOffset + sectionSize
		shiftedOffset := nextSectionOffset << pciExtendedCapabilityOffsetShift
		result = binary.LittleEndian.AppendUint16(result, uint16(id))
		result = binary.LittleEndian.AppendUint16(result, uint16(shiftedOffset))
		result = append(result, make([]byte, dataSize)...)
	}
	result = binary.LittleEndian.AppendUint16(result, uint16(pciExtendedCapabilityDsnID)) // 16-bit capability id
	result = binary.LittleEndian.AppendUint16(result, uint16(1))                          // 16-bit capability size (shiftedl
	result = binary.LittleEndian.AppendUint64(result, id)
	return result
}

func Test_getPCIClockID(t *testing.T) {
	mfs, restore := setupMockFS()
	defer restore()

	notFound := uint64(0)

	// No such file
	mfs.ExpectReadFile("/sys/class/net/missing/device/config", []byte{}, fmt.Errorf("No such file"))
	clockID := getPCIClockID("missing")
	assert.Equal(t, notFound, clockID)
	mfs.VerifyAllCalls(t)

	// Config empty
	mfs.ExpectReadFile("/sys/class/net/short/device/config", []byte{}, nil)
	clockID = getPCIClockID("short")
	assert.Equal(t, notFound, clockID)
	mfs.VerifyAllCalls(t)

	// No DSN
	mfs.ExpectReadFile("/sys/class/net/empty/device/config", make([]byte, pciConfigSpaceSize+64), nil)
	clockID = getPCIClockID("empty")
	assert.Equal(t, notFound, clockID)
	mfs.VerifyAllCalls(t)

	// DSN at start of space
	expectedID := uint64(1111222233334)
	mfs.ExpectReadFile("/sys/class/net/one/device/config", generatePCIDataForClockID(expectedID), nil)
	clockID = getPCIClockID("one")
	assert.Equal(t, expectedID, clockID)
	mfs.VerifyAllCalls(t)

	// DSN after a few other sections
	expectedID = uint64(5555666677778)
	mfs.ExpectReadFile("/sys/class/net/two/device/config", generateTestPCIDataForClockID(expectedID, 3), nil)
	clockID = getPCIClockID("two")
	assert.Equal(t, expectedID, clockID)
	mfs.VerifyAllCalls(t)

	// Config space truncated
	expectedID = uint64(7777888899990)
	fullData := generateTestPCIDataForClockID(expectedID, 2)
	mfs.ExpectReadFile("/sys/class/net/truncated/device/config", fullData[:len(fullData)-16], nil)
	clockID = getPCIClockID("truncated")
	assert.Equal(t, notFound, clockID)
	mfs.VerifyAllCalls(t)
}
