package constants

// PTPPortRole ...
type PTPPortRole int

const (
	// PortRolePassive ...
	PortRolePassive PTPPortRole = iota
	// PortRoleSlave ...
	PortRoleSlave
	// PortRoleMaster ...
	PortRoleMaster
	// PortRoleFaulty ...
	PortRoleFaulty
	// PortRoleUnknown ...
	PortRoleUnknown
	// PortRoleListening ...
	PortRoleListening
)

func (pr PTPPortRole) String() string {
	switch pr {
	case PortRoleSlave:
		return "SLAVE"
	case PortRoleMaster:
		return "MASTER"
	case PortRolePassive:
		return "PASSIVE"
	case PortRoleFaulty:
		return "FAULTY"
	case PortRoleListening:
		return "LISTENING"
	default:
		return "UNKNOWN"
	}
}
