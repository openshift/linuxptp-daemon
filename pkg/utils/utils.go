package utils

import (
	"regexp"

	log "github.com/sirupsen/logrus"
)

// GetAlias generates a PHC (PTP Hardware Clock) identifier alias from network interface names.
// It supports Intel and Mellanox naming formats with optional VLAN tags.
//
// Supported formats:
//   - Intel: eth0 -> ethx, ens1f0 -> ens1fx, ens1f0.100 -> ens1fx.100
//   - Mellanox: enP2s2f0np0 -> enP2s2fx, enP2s2f0np0.100 -> enP2s2fx.100
//
// For unsupported formats, returns the original interface name and logs an error.
//
// Parameters:
//   - ifname: Network interface name (e.g., "ens1f0", "enP2s2f0np0", "eth0.100")
//
// Returns:
//   - Alias string for PHC identification, or original name if format is unsupported
func GetAlias(ifname string) string {
	alias := ""
	if ifname != "" {
		// Single regex to handle both Intel and Mellanox formats with optional VLAN
		// Intel format: ens1f0, eth0, ens1f0.100 -> ens1fx, ethx, ens1fx.100
		// Mellanox format: enP2s2f0np0, enP2s2f0np0.100 -> enP2s2fx, enP2s2fx.100
		pattern := regexp.MustCompile(`^(.+?)(\d+)(?:np\d+)?(\..+)?$`)
		matches := pattern.FindStringSubmatch(ifname)

		if len(matches) >= 3 {
			// matches[1] contains the prefix (everything before the last digit sequence)
			// matches[2] contains the digit sequence to replace
			// matches[3] contains the VLAN part (including the dot) or empty string
			alias = matches[1] + "x"
			if len(matches) > 3 && matches[3] != "" {
				alias += matches[3] // append VLAN part if present
			}
		} else {
			// Interface doesn't match Intel or Mellanox format, return original interface name
			log.Errorf("Interface %s does not match Intel or Mellanox naming format, using original interface name", ifname)
			alias = ifname
		}
	}
	return alias
}
