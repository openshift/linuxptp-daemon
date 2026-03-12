// Package intel contains plugins for all supported Intel NICs
package intel

import (
	"os"
	"slices"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
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
	name      string
	hwplugins []string
}

// PopulateHwConfig populates hwconfig for all intel plugins
func (data *PluginData) PopulateHwConfig(_ *interface{}, hwconfigs *[]ptpv1.HwConfig) error {
	for _, _hwconfig := range data.hwplugins {
		hwConfig := ptpv1.HwConfig{}
		hwConfig.DeviceID = data.name
		hwConfig.Status = _hwconfig
		*hwconfigs = append(*hwconfigs, hwConfig)
	}
	return nil
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
	ReadLink(filename string) (string, error)
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

// ReadLink returns the destination of a symbolic link.
func (fs *RealFileSystem) ReadLink(filename string) (string, error) {
	return os.Readlink(filename)
}

// Default filesystem implementation
var filesystem FileSystemInterface = &RealFileSystem{}

// GnssOptions defines GNSS-specific options common to Intel NIC plugins
type GnssOptions struct {
	Disabled    bool              `json:"disabled"`
	LeapSources LeapSourceOptions `json:"leapSources"`
}

// LeapSourceOptions defines enabled or disabled leap second sources
type LeapSourceOptions struct {
	GPS     bool `json:"gps"`
	GLONASS bool `json:"glonass"`
	BeiDou  bool `json:"beidou"`
	Galileo bool `json:"galileo"`
	SBAS    bool `json:"sbas"`
	NavIC   bool `json:"navic"`
}

// AllowedLeapSources converts the boolean flags into a map of
// ublox leap source IDs suitable for leap.SetAllowedLeapSources.
func (o LeapSourceOptions) AllowedLeapSources() map[uint8]bool {
	mappings := []struct {
		enabled bool
		id      uint8
	}{
		{o.GPS, ublox.LeapSourceGPS},
		{o.GLONASS, ublox.LeapSourceGLONASS},
		{o.BeiDou, ublox.LeapSourceBeiDou},
		{o.Galileo, ublox.LeapSourceGalileo},
		{o.SBAS, ublox.LeapSourceSBAS},
		{o.NavIC, ublox.LeapSourceNavIC},
	}

	sources := make(map[uint8]bool)
	for _, m := range mappings {
		if m.enabled {
			sources[m.id] = true
		}
	}
	return sources
}

// defaultLeapSourceOptions returns LeapSourceOptions with all sources enabled.
func defaultLeapSourceOptions() LeapSourceOptions {
	return LeapSourceOptions{
		GPS: true, GLONASS: true, BeiDou: true,
		Galileo: true, SBAS: true, NavIC: true,
	}
}

// updateLeapManagerSources configures the LeapManager with the given leap source options.
func updateLeapManagerSources(sources LeapSourceOptions) {
	if leap.LeapMgr != nil {
		leap.LeapMgr.SetAllowedLeapSources(sources.AllowedLeapSources())
	}
}
