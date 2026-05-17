package alias

const helpText = "You might want to consider enforcing path or slot based names with systemd udev rules"

// GetAlias ...
func GetAlias(ifname string) string {
	return storeInstance.getAlias(ifname)
}

// SetAlias ...
func SetAlias(ifname, alias string) {
	storeInstance.setAlias(ifname, alias)
}

// AddInterface ...
func AddInterface(phc, ifname string) {
	storeInstance.addInterface(phc, ifname)
}

// CalculateAliases ...
func CalculateAliases() {
	storeInstance.calculateAliases()
}

// ClearAliases ...
func ClearAliases() {
	storeInstance.clear()
}

// GetAllAliases returns a copy of all interface-to-alias mappings
func GetAllAliases() map[string]string {
	return storeInstance.getAllAliases()
}

// GetPhcGroup returns the PHC device path for the given interface, or empty
// string if the interface was not registered. Two interfaces with the same
// non-empty return value share the same PHC hardware.
func GetPhcGroup(ifname string) string {
	return storeInstance.getPhcGroup(ifname)
}

// Debug ...
func Debug(logF func(string, ...any)) {
	for ifName, alias := range storeInstance.aliases {
		logF("DEBUG: ifname: '%s' alias: '%s'\n", ifName, alias)
	}
}
