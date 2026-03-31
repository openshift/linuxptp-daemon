package intel

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_applyPinSet(t *testing.T) {
	mockFS, restoreFS := setupMockFS()
	defer restoreFS()

	err := pinConfig.applyPinSet("device", pinSet{})
	assert.Error(t, err)

	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
	mockFS.ExpectReadDir("", phcEntries, nil)

	err = pinConfig.applyPinSet("device", pinSet{})
	assert.NoError(t, err)
	assert.Equal(t, 0, mockFS.currentWriteFile)

	mockFS.ExpectReadDir("", phcEntries, nil)
	err = pinConfig.applyPinSet("device", pinSet{
		"BAD": "1 0",
	})
	assert.Error(t, err)

	mockFS.ExpectReadDir("", phcEntries, nil)
	mockFS.ExpectWriteFile("/sys/class/net/device/device/ptp/ptp0/pins/PIN", []byte{}, 0o666, nil)
	err = pinConfig.applyPinSet("device", pinSet{
		"PIN": "1 0",
	})
	assert.NoError(t, err)
	assert.Equal(t, 1, mockFS.currentWriteFile)
}

func Test_hasSysfsSMAPins(t *testing.T) {
	t.Run("SMA1 exists", func(t *testing.T) {
		mockFS, restoreFS := setupMockFS()
		defer restoreFS()
		phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
		mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
		mockFS.ExpectReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", []byte("0 1"), nil)
		assert.True(t, hasSysfsSMAPins("ens4f0"))
	})

	t.Run("SMA1 missing", func(t *testing.T) {
		mockFS, restoreFS := setupMockFS()
		defer restoreFS()
		phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
		mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", phcEntries, nil)
		mockFS.ExpectReadFile("/sys/class/net/ens4f0/device/ptp/ptp0/pins/SMA1", nil, os.ErrNotExist)
		assert.False(t, hasSysfsSMAPins("ens4f0"))
	})

	t.Run("no PHC directory", func(t *testing.T) {
		mockFS, restoreFS := setupMockFS()
		defer restoreFS()
		mockFS.ExpectReadDir("/sys/class/net/ens4f0/device/ptp/", nil, os.ErrNotExist)
		assert.False(t, hasSysfsSMAPins("ens4f0"))
	})
}

func Test_applyPinFrq(t *testing.T) {
	mockFS, restoreFS := setupMockFS()
	defer restoreFS()

	err := pinConfig.applyPinFrq("device", frqSet{})
	assert.Error(t, err)

	phcEntries := []os.DirEntry{MockDirEntry{name: "ptp0", isDir: true}}
	mockFS.ExpectReadDir("", phcEntries, nil)

	err = pinConfig.applyPinFrq("device", frqSet{})
	assert.NoError(t, err)
	assert.Equal(t, 0, mockFS.currentWriteFile)

	mockFS.ExpectReadDir("", phcEntries, nil)
	err = pinConfig.applyPinFrq("device", frqSet{
		"1 0 0 1 0",
	})
	assert.Error(t, err)

	mockFS.ExpectReadDir("", phcEntries, nil)
	mockFS.ExpectWriteFile("/sys/class/net/device/device/ptp/ptp0/period", []byte{}, 0o666, nil)
	err = pinConfig.applyPinFrq("device", frqSet{
		"1 0 0 1 0",
	})
	assert.NoError(t, err)
	assert.Equal(t, 1, mockFS.currentWriteFile)
}
