package intel

import (
	"errors"
	"os"
	"testing"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/yaml"
)

// mockBatchPinSet is a simple mock to unit-test pin set operations
type mockBatchPinSet struct {
	commands *[]dpll.PinParentDeviceCtl
}

func (m *mockBatchPinSet) mock(commands *[]dpll.PinParentDeviceCtl) error {
	m.commands = commands
	return nil
}

func (m *mockBatchPinSet) reset() {
	m.commands = nil
}

func setupBatchPinSetMock() (*mockBatchPinSet, func()) {
	originalBatchPinset := BatchPinSet
	mock := &mockBatchPinSet{}
	BatchPinSet = mock.mock
	return mock, func() { BatchPinSet = originalBatchPinset }
}

// MockFileSystem is a simple mock implementation of FileSystemInterface
type MockFileSystem struct {
	// Expected calls and responses
	readDirCalls     []ReadDirCall
	writeFileCalls   []WriteFileCall
	readFileCalls    []ReadFileCall
	currentReadDir   int
	currentWriteFile int
	currentReadFile  int
}

func setupMockFS() (*MockFileSystem, func()) {
	originalFilesystem := filesystem
	mock := &MockFileSystem{}
	filesystem = mock
	return mock, func() { filesystem = originalFilesystem }
}

type ReadDirCall struct {
	expectedPath string
	returnDirs   []os.DirEntry
	returnError  error
}

type WriteFileCall struct {
	expectedPath string
	expectedData []byte
	expectedPerm os.FileMode
	returnError  error
}

type ReadFileCall struct {
	expectedPath string
	returnData   []byte
	returnError  error
}

func (m *MockFileSystem) ExpectReadDir(path string, dirs []os.DirEntry, err error) {
	m.readDirCalls = append(m.readDirCalls, ReadDirCall{
		expectedPath: path,
		returnDirs:   dirs,
		returnError:  err,
	})
}

func (m *MockFileSystem) ExpectWriteFile(path string, data []byte, perm os.FileMode, err error) {
	m.writeFileCalls = append(m.writeFileCalls, WriteFileCall{
		expectedPath: path,
		expectedData: data,
		expectedPerm: perm,
		returnError:  err,
	})
}

func (m *MockFileSystem) ExpectReadFile(path string, data []byte, err error) {
	m.readFileCalls = append(m.readFileCalls, ReadFileCall{
		expectedPath: path,
		returnData:   data,
		returnError:  err,
	})
}

func (m *MockFileSystem) ReadDir(dirname string) ([]os.DirEntry, error) {
	if m.currentReadDir >= len(m.readDirCalls) {
		return nil, errors.New("unexpected ReadDir call")
	}
	call := m.readDirCalls[m.currentReadDir]
	m.currentReadDir++
	// Allow wildcard matching - if expectedPath is empty, accept any path
	if call.expectedPath != "" && call.expectedPath != dirname {
		return nil, errors.New("ReadDir called with unexpected path")
	}
	return call.returnDirs, call.returnError
}

func (m *MockFileSystem) WriteFile(filename string, _ []byte, _ os.FileMode) error {
	if m.currentWriteFile >= len(m.writeFileCalls) {
		return errors.New("unexpected WriteFile call")
	}
	call := m.writeFileCalls[m.currentWriteFile]
	m.currentWriteFile++
	if call.expectedPath != "" && call.expectedPath != filename {
		return errors.New("WriteFile called with unexpected path")
	}
	return call.returnError
}

func (m *MockFileSystem) ReadFile(filename string) ([]byte, error) {
	if m.currentReadFile >= len(m.readFileCalls) {
		return nil, errors.New("Unexpected ReadFile call")
	}
	call := m.readFileCalls[m.currentReadFile]
	m.currentReadFile++
	if call.expectedPath != "" && call.expectedPath != filename {
		return nil, errors.New("ReadFile called with unexpected filename")
	}
	return call.returnData, call.returnError
}

func (m *MockFileSystem) VerifyAllCalls(t *testing.T) {
	assert.Equal(t, len(m.readDirCalls), m.currentReadDir, "Not all expected ReadDir calls were made")
	assert.Equal(t, len(m.writeFileCalls), m.currentWriteFile, "Not all expected WriteFile calls were made")
	assert.Equal(t, len(m.readFileCalls), m.currentReadFile, "Not all expected ReadFile calls were made")
}

// MockDirEntry implements os.DirEntry for testing
type MockDirEntry struct {
	name  string
	isDir bool
}

func (m MockDirEntry) Name() string               { return m.name }
func (m MockDirEntry) IsDir() bool                { return m.isDir }
func (m MockDirEntry) Type() os.FileMode          { return 0 }
func (m MockDirEntry) Info() (os.FileInfo, error) { return nil, nil }

func loadProfile(path string) (*ptpv1.PtpProfile, error) {
	profileData, err := os.ReadFile(path)
	if err != nil {
		return &ptpv1.PtpProfile{}, err
	}
	profile := ptpv1.PtpProfile{}
	err = yaml.Unmarshal(profileData, &profile)
	if err != nil {
		return &ptpv1.PtpProfile{}, err
	}
	return &profile, nil
}
