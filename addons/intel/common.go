// Package intel contains plugins for all supported Intel NICs
package intel

import (
	"os"
	"slices"
)

// PluginOpts contains all configuration data common to all addons/intel NIC plugins
type PluginOpts struct {
	Devices          []string                     `json:"devices"`
	DevicePins       map[string]pinSet            `json:"pins"`
	DeviceFreqencies map[string]frqSet            `json:"frequencies"`
	DpllSettings     map[string]uint64            `json:"settings"`
	PhaseOffsetPins  map[string]map[string]string `json:"phaseOffsetPins"`
}

// PluginData contains all persistent data commont to all addons/intel NIC plugins
type PluginData struct {
	hwplugins []string
}

func extendWithKeys[T any](s []string, m map[string]T) []string {
	for key := range m {
		if !slices.Contains(s, key) {
			s = append(s, key)
		}
	}
	return s
}

// allDevices enumerates all defined devices (Devices/DevicePins/DeviceFrequencies/PhaseOffsets)
func (opts *PluginOpts) allDevices() []string {
	// Enumerate all defined devices (Devices/DevicePins/DeviceFrequencies)
	allDevices := opts.Devices
	allDevices = extendWithKeys(allDevices, opts.DevicePins)
	allDevices = extendWithKeys(allDevices, opts.DeviceFreqencies)
	allDevices = extendWithKeys(allDevices, opts.PhaseOffsetPins)
	return allDevices
}

// FileSystemInterface defines the interface for filesystem operations to enable mocking
type FileSystemInterface interface {
	ReadDir(dirname string) ([]os.DirEntry, error)
	WriteFile(filename string, data []byte, perm os.FileMode) error
	ReadFile(filename string) ([]byte, error)
}

// RealFileSystem implements FileSystemInterface using real OS operations
type RealFileSystem struct{}

// ReadDir reads the contents of the directory specified by dirname
func (fs *RealFileSystem) ReadDir(dirname string) ([]os.DirEntry, error) {
	return os.ReadDir(dirname)
}

// WriteFile writes the data to the file specified by filename
func (fs *RealFileSystem) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return os.WriteFile(filename, data, perm)
}

// ReadFile reads the data from the file specified by the filename
func (fs *RealFileSystem) ReadFile(filename string) ([]byte, error) {
	return os.ReadFile(filename)
}

// Default filesystem implementation
var filesystem FileSystemInterface = &RealFileSystem{}
