// Package intel contains plugins for all supported Intel NICs
package intel

import (
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/leap"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/ublox"
)

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
