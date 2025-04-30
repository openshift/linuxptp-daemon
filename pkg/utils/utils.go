package utils

import "strings"

// GetAlias masks interface names for metric reporting
func GetAlias(ifname string) string {
	alias := ""
	if ifname != "" {
		// Aliases the interface name or <interface_name>.<vlan>
		dotIndex := strings.Index(ifname, ".")
		if dotIndex == -1 {
			// e.g. ens1f0 -> ens1fx
			alias = ifname[:len(ifname)-1] + "x"
		} else {
			// e.g ens1f0.100 -> ens1fx.100
			alias = ifname[:dotIndex-1] + "x" + ifname[dotIndex:]
		}
	}
	return alias
}
