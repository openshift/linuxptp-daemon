package debug

import (
	"fmt"
	"strings"
)

const (
	DpllKey          = "DPLL"
	Ts2phcKey        = "TS2PHC"
	GnssKey          = "GNSS"
	GmKey            = "GM"
	OverallTs2phcKey = "OVER-ALL-TS2PHC"
	OverallDpllKey   = "OVER-ALL-DPLL"
	ClockClassKey    = "CC"
)

// Define some Unicode symbols for the icons
var (
	iconLocked     = "Locked"   // GNSS lock
	iconFreeRun    = "FreeRun"  // Open lock
	iconHoldOver   = "HoldOver" // Hold icon
	iconAntenna    = "GNSS"
	ts2phcIcon     = "ts2phc"
	iconClockClass = "ClockClass"
	dpllIcon       = "dpll"
	state          = map[string]StateDB{}
	requiredKeys   = []string{GmKey, GnssKey, OverallTs2phcKey, OverallDpllKey, ClockClassKey}
	resetGM        bool
)

// StateDB stores the state of the system
type StateDB struct {
	State  interface{}
	Offset interface{}
}

// getSate returns the icon corresponding to the given state string
func getSate(s string) string {
	ss := ""
	switch s {
	case "s0":
		ss = iconFreeRun
	case "s2", "s3":
		ss = iconLocked
	case "s1":
		ss = iconHoldOver
	}
	return ss
}

// Node represents each node's state and offset
type Node struct {
	name   string
	state  interface{}
	iface  string
	offset interface{}
}

// UpdateGMState updates the state of the Grand Master Clock (GM) and prints the tree if necessary
func UpdateGMState(s string) {
	if st, ok := state[GmKey]; !ok || st.State != s || resetGM {
		state[GmKey] = StateDB{State: s}
		resetGM = false
		PrintTree()
	}
}

// UpdateTs2phcState updates the state of the TS2PHC and sets resetGM to true if necessary
func UpdateTs2phcState(s string, offset interface{}, iface string) {
	key := Ts2phcKey + "-" + iface
	if iface == OverallTs2phcKey {
		key = iface
	}
	if st, ok := state[key]; !ok || st.State.(string) != s {
		state[key] = StateDB{State: s, Offset: offset}
		resetGM = true
	}
}

// UpdateDPLLState updates the state of the DPLL and sets resetGM to true if necessary
func UpdateDPLLState(s string, offset interface{}, iface string) {
	key := DpllKey + "-" + iface
	if iface == OverallDpllKey {
		key = iface
	}
	if st, ok := state[key]; !ok || st.State.(string) != s {
		state[key] = StateDB{State: s, Offset: offset}
		resetGM = true
	}
}

// UpdateGNSSState updates the state of the GNSS and sets resetGM to true if necessary
func UpdateGNSSState(s string, offset interface{}) {
	if st, ok := state[GnssKey]; !ok || st.State.(string) != s {
		state[GnssKey] = StateDB{State: s, Offset: offset}
		resetGM = true
	}
}

// UpdateClockClass updates the state of the Clock Class and sets resetGM to true if necessary
func UpdateClockClass(ClassTo uint8) {
	if st, ok := state[ClockClassKey]; !ok || ClassTo != st.State.(uint8) {
		state[ClockClassKey] = StateDB{State: ClassTo}
		resetGM = true
	}
}

// printTreeNode prints a tree node with its children
func printTreeNode(parent Node, children []Node, indent string, isLast bool) {
	// Print the parent node with the appropriate indent
	connector := "├──"
	if isLast {
		connector = "└──"
	}

	fmt.Printf("%s%s %s (State:%v)\n", indent, connector, parent.name, parent.state)

	// Print the children nodes with an increased indent
	childIndent := indent + "    "
	for i, child := range children {
		isLastChild := i == len(children)-1
		printTreeNode(child, nil, childIndent, isLastChild)
		fmt.Printf("%s    └── State: %s Iface: %s  Offset %v\n", childIndent, child.state, child.iface, child.offset)
	}
}

// printTree prints the entire tree with its nodes
func printTree(root Node, rootChildren map[Node][]Node) {
	// Print the root node (GM)
	fmt.Println(root.name + " (State:" + root.state.(string) + ")")
	i := 0
	for child, children := range rootChildren {
		isLast := i == len(rootChildren)-1
		printTreeNode(child, children, "*", isLast)
		i++
	}
}

// PrintTree prints the tree if all required keys are present
func PrintTree() {
	if !hasAllKeys(requiredKeys) {
		return
	}
	clockClass := Node{
		name:  iconClockClass,
		state: state[ClockClassKey].State,
	}

	ss := getSate(state[GmKey].State.(string))
	root := Node{
		name:  "GM (Grand Master Clock)",
		state: ss, // GM is synchronized
	}
	ss = getSate(state[GnssKey].State.(string))
	gnss := Node{
		name:  iconAntenna,
		state: ss,
	}

	// Define the child nodes (ts2phc and DPLL)
	ss = getSate(state[OverallTs2phcKey].State.(string))
	ts2phc := Node{
		name:  OverallTs2phcKey,
		state: ss,
	}
	ss = getSate(state[OverallDpllKey].State.(string))
	dpll := Node{
		name:  OverallDpllKey,
		state: ss,
	}

	// Define child nodes for ts2phc and DPLL
	ts2phcChildren := []Node{}
	dpllChildren := []Node{}

	for k, v := range state {
		if k == OverallTs2phcKey || k == OverallDpllKey || k == GmKey || k == GnssKey || k == ClockClassKey {
			continue
		}
		ss = getSate(v.State.(string))
		if strings.Split(k, "-")[0] == Ts2phcKey {
			ts2phcChildren = append(ts2phcChildren, Node{name: ts2phcIcon, state: ss, iface: strings.Split(k, "-")[1], offset: v.Offset})
		} else if strings.Split(k, "-")[0] == DpllKey {
			dpllChildren = append(dpllChildren, Node{name: dpllIcon, state: ss, iface: strings.Split(k, "-")[1], offset: v.Offset})
		}
	}
	// Map the children to their respective parent nodes
	rootChildren := map[Node][]Node{
		gnss:       {},
		clockClass: {},
		ts2phc:     ts2phcChildren,
		dpll:       dpllChildren,
	}

	// Print the tree
	printTree(root, rootChildren)
}

// hasAllKeys checks if all required keys are present in the state
func hasAllKeys(keys []string) bool {
	for _, key := range keys {
		if _, ok := state[key]; !ok {
			return false
		}
	}
	return true
}

// ClearState clears the state and resets resetGM
func ClearState() {
	state = map[string]StateDB{}
	resetGM = false
}
