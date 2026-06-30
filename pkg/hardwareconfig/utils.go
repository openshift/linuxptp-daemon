package hardwareconfig

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/mdlayher/netlink"

	dpll "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/dpll-netlink"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

// Stable kernel UAPI constants for DPLL pin resolution via RTM_GETLINK.
const (
	netlinkRoute    = 0  // NETLINK_ROUTE
	rtmGetLink      = 18 // RTM_GETLINK
	rtmNewLink      = 16 // RTM_NEWLINK
	sizeofIfInfomsg = 16 // sizeof(struct ifinfomsg)
	iflaDpllPin     = 65 // IFLA_DPLL_PIN
)

// DpllPinsGetter is a function type for getting DPLL pins
type DpllPinsGetter func() (*PinCache, error)

// defaultDpllPinsGetter is the default implementation that connects to real DPLL
var defaultDpllPinsGetter DpllPinsGetter = getRealDpllPins

// dpllPinsGetter holds the current implementation (can be swapped for testing)
var dpllPinsGetter = defaultDpllPinsGetter

// SetDpllPinsGetter allows tests to inject a mock implementation
func SetDpllPinsGetter(getter DpllPinsGetter) {
	dpllPinsGetter = getter
}

// ResetDpllPinsGetter resets to the default implementation
func ResetDpllPinsGetter() {
	dpllPinsGetter = defaultDpllPinsGetter
}

// GetDpllPins returns the DPLL pin cache using the current getter implementation
func GetDpllPins() (*PinCache, error) {
	return dpllPinsGetter()
}

// PinCache is a cache of DPLL pins with O1 access, hashed by clock ID and board label
type PinCache struct {
	Pins map[uint64]map[string]dpll.PinInfo
}

// ClockIDResolver resolves clock ID from network interface
type ClockIDResolver func(string, string) (uint64, error)

// GetSubsystemNetworkInterface retrieves the network interface for a given subsystem.
// It first checks if the subsystem has a NetworkInterface explicitly set,
// and falls back to the first Ethernet port if not specified.
func GetSubsystemNetworkInterface(clockChain *ptpv2alpha1.ClockChain, subsystemName string) (string, error) {
	for _, subsystem := range clockChain.Structure {
		if subsystem.Name == subsystemName {
			networkInterface := subsystem.DPLL.NetworkInterface
			if networkInterface == "" {
				// Fall back to first ethernet port
				if len(subsystem.Ethernet) > 0 && len(subsystem.Ethernet[0].Ports) > 0 {
					networkInterface = subsystem.Ethernet[0].Ports[0]
				}
			}
			if networkInterface == "" {
				return "", fmt.Errorf("no network interface found for subsystem %s", subsystemName)
			}
			return networkInterface, nil
		}
	}

	return "", fmt.Errorf("subsystem %s not found in clock chain", subsystemName)
}

// getSubsystemHardwareDefinition retrieves the hardware-specific definition path for a given subsystem.
// Returns the trimmed hardware definition path and a boolean indicating whether it was specified.
func getSubsystemHardwareDefinition(clockChain *ptpv2alpha1.ClockChain, subsystemName string) (string, bool, error) {
	for _, subsystem := range clockChain.Structure {
		if subsystem.Name == subsystemName {
			hwDefPath := strings.TrimSpace(subsystem.HardwareSpecificDefinitions)
			if hwDefPath == "" {
				return "", false, nil
			}
			return hwDefPath, true, nil
		}
	}

	return "", false, fmt.Errorf("subsystem %s not found in clock chain", subsystemName)
}

// CommandExecutor is an interface for executing system commands
// This allows for easy mocking in unit tests
type CommandExecutor interface {
	Execute(command string, args ...string) (string, error)
}

// RealCommandExecutor executes real system commands
type RealCommandExecutor struct{}

// Execute runs a command and returns its output
func (r *RealCommandExecutor) Execute(command string, args ...string) (string, error) {
	cmd := exec.Command(command, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("command %s %v failed: %w, output: %s", command, args, err, string(output))
	}
	return strings.TrimSpace(string(output)), nil
}

// commandExecutor is the global executor that can be swapped for testing
var commandExecutor CommandExecutor = &RealCommandExecutor{}

// SetCommandExecutor allows tests to inject a mock command executor
func SetCommandExecutor(executor CommandExecutor) {
	commandExecutor = executor
}

// ResetCommandExecutor resets to the default real command executor
func ResetCommandExecutor() {
	commandExecutor = &RealCommandExecutor{}
}

// MockCommandExecutor implements CommandExecutor for testing
// This is exported so other packages can use it in their tests
type MockCommandExecutor struct {
	// Map of command+args to output
	Responses map[string]string
	Errors    map[string]error
	CallLog   []string
}

// NewMockCommandExecutor creates a new mock command executor
func NewMockCommandExecutor() *MockCommandExecutor {
	return &MockCommandExecutor{
		Responses: make(map[string]string),
		Errors:    make(map[string]error),
		CallLog:   make([]string, 0),
	}
}

// Execute implements CommandExecutor interface
func (m *MockCommandExecutor) Execute(command string, args ...string) (string, error) {
	key := fmt.Sprintf("%s %v", command, args)
	m.CallLog = append(m.CallLog, key)

	if err, hasErr := m.Errors[key]; hasErr {
		return "", err
	}

	if response, hasResponse := m.Responses[key]; hasResponse {
		return response, nil
	}

	return "", fmt.Errorf("no mock response for command: %s", key)
}

// SetResponse sets a mock response for a command
func (m *MockCommandExecutor) SetResponse(command string, args []string, response string) {
	key := fmt.Sprintf("%s %v", command, args)
	m.Responses[key] = response
}

// SetError sets a mock error for a command
func (m *MockCommandExecutor) SetError(command string, args []string, err error) {
	key := fmt.Sprintf("%s %v", command, args)
	m.Errors[key] = err
}

// GetCallLog returns the log of all commands executed
func (m *MockCommandExecutor) GetCallLog() []string {
	return m.CallLog
}

// ClockIDTransformer defines how to transform a serial number to a clock ID for specific hardware
type ClockIDTransformer func(serialNumber string) (uint64, error)

// getClockIDTransformer returns the appropriate transformer based on hardware defaults.
// If hwDefPath is empty or invalid, falls back to EUI-64 (generic).
func getClockIDTransformer(hwDefPath string) ClockIDTransformer {
	if hwDefPath != "" {
		spec, err := LoadHardwareDefaults(hwDefPath, nil)
		if err == nil && spec != nil && spec.ClockIDTransformation != nil {
			method := spec.ClockIDTransformation.Method
			switch method {
			case "direct":
				glog.V(4).Infof("Using direct clock ID transformation (hwDef: %s)", hwDefPath)
				return parseSerialNumberToClockIDDirect
			case "eui64":
				glog.V(4).Infof("Using EUI-64 clock ID transformation (hwDef: %s)", hwDefPath)
				return parseSerialNumberToClockIDEUI64
			default:
				glog.Warningf("Unknown clock ID transformation method %s, using default", method)
			}
		} else if err != nil {
			glog.Warningf("Failed to load hardware defaults from %s: %v; using default", hwDefPath, err)
		}
	}
	// Default to EUI-64 (generic) transformation
	return parseSerialNumberToClockIDEUI64
}

// GetClockIDFromInterface resolves clock ID from network interface using ethtool and devlink.
// The hardware definition path (when provided) determines how the serial number is transformed.
// For PERLA hardware (E825 with zl3073x DPLL), uses a workaround to associate NIC with DPLL clock ID.
func GetClockIDFromInterface(iface string, hwDefPath string) (uint64, error) {
	return GetClockIDFromInterfaceWithCache(iface, hwDefPath, nil)
}

// getNetdevDpllPin retrieves the DPLL pin ID associated with a network interface
// via the kernel's IFLA_DPLL_PIN netlink attribute.
func getNetdevDpllPin(ifname string) (uint32, bool, error) {
	iface, err := net.InterfaceByName(ifname)
	if err != nil {
		return 0, false, fmt.Errorf("could not find interface %s: %w", ifname, err)
	}

	conn, err := netlink.Dial(netlinkRoute, nil)
	if err != nil {
		return 0, false, fmt.Errorf("failed to dial netlink route: %w", err)
	}
	defer conn.Close()

	b := make([]byte, sizeofIfInfomsg)
	binary.NativeEndian.PutUint32(b[4:8], uint32(iface.Index))

	msg := netlink.Message{
		Header: netlink.Header{
			Type:  rtmGetLink,
			Flags: netlink.Request,
		},
		Data: b,
	}

	replyMsgs, err := conn.Execute(msg)
	if err != nil {
		return 0, false, fmt.Errorf("failed to execute netlink request for %s: %w", ifname, err)
	}

	if len(replyMsgs) != 1 {
		return 0, false, fmt.Errorf("expected 1 reply message for %s, got %d", ifname, len(replyMsgs))
	}
	reply := replyMsgs[0]

	if reply.Header.Type != rtmNewLink {
		return 0, false, fmt.Errorf("expected RTM_NEWLINK for %s, got %d", ifname, reply.Header.Type)
	}
	if len(reply.Data) < sizeofIfInfomsg {
		return 0, false, fmt.Errorf("reply too short for %s", ifname)
	}

	ad, err := netlink.NewAttributeDecoder(reply.Data[sizeofIfInfomsg:])
	if err != nil {
		return 0, false, fmt.Errorf("failed to create attribute decoder: %w", err)
	}

	var dpllPinID uint32
	var found bool

	for ad.Next() {
		if ad.Type() == iflaDpllPin {
			ad.Nested(func(nad *netlink.AttributeDecoder) error {
				for nad.Next() {
					if nad.Type() == dpll.DpllPinID {
						dpllPinID = nad.Uint32()
						found = true
						return nil
					}
				}
				return nad.Err()
			})
			if found {
				break
			}
		}
	}

	if adErr := ad.Err(); adErr != nil {
		return 0, false, fmt.Errorf("attribute decoding failed for %s: %w", ifname, adErr)
	}

	return dpllPinID, found, nil
}

// netdevDpllPinGetter is swappable for testing.
var netdevDpllPinGetter = getNetdevDpllPin

// queryDpllPin queries a single DPLL pin by ID using a new DPLL netlink connection.
func queryDpllPin(pinID uint32) (*dpll.PinInfo, error) {
	conn, err := dpll.Dial(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial DPLL: %w", err)
	}
	defer conn.Close()
	return conn.DoPinGet(dpll.DoPinGetRequest{ID: pinID})
}

// dpllPinQuerier is swappable for testing.
var dpllPinQuerier = queryDpllPin

// getClockIDFromPinParentChain derives the external DPLL clock ID for a NIC
// by tracing the DPLL pin parentage chain: NIC → DPLL pin → parent pins →
// external DPLL module → clock ID.
func getClockIDFromPinParentChain(ifname string) (uint64, error) {
	glog.Infof("Attempting NIC-to-DPLL derivation for interface %s", ifname)

	pinID, found, err := netdevDpllPinGetter(ifname)
	if err != nil {
		return 0, fmt.Errorf("failed to get DPLL pin for %s: %w", ifname, err)
	}
	if !found {
		return 0, fmt.Errorf("no DPLL pin found for interface %s", ifname)
	}
	glog.Infof("NIC %s DPLL pin ID: %d", ifname, pinID)

	nicPin, err := dpllPinQuerier(pinID)
	if err != nil {
		return 0, fmt.Errorf("failed to query DPLL pin %d: %w", pinID, err)
	}

	if len(nicPin.ParentPin) == 0 {
		return 0, fmt.Errorf("DPLL pin %d for %s has no parent pins", pinID, ifname)
	}

	parentIDs := make([]uint32, len(nicPin.ParentPin))
	for i, p := range nicPin.ParentPin {
		parentIDs[i] = p.ParentID
	}
	glog.Infof("DPLL pin %d has %d parent pins: %v", pinID, len(nicPin.ParentPin), parentIDs)

	for _, parent := range nicPin.ParentPin {
		parentPin, parentErr := dpllPinQuerier(parent.ParentID)
		if parentErr != nil {
			glog.Warningf("Failed to query parent pin %d: %v, trying next", parent.ParentID, parentErr)
			continue
		}
		if parentPin.ModuleName != nicPin.ModuleName {
			glog.Infof("Found external DPLL parent pin %d (module=%s) with clock ID %d for interface %s",
				parent.ParentID, parentPin.ModuleName, parentPin.ClockID, ifname)
			return parentPin.ClockID, nil
		}
	}

	return 0, fmt.Errorf("no external DPLL parent found for pin %d on %s (all parents share module %q)",
		pinID, ifname, nicPin.ModuleName)
}

func getPERLAClockIDFromPinCache(cache *PinCache) (uint64, error) {
	if cache == nil {
		var cacheErr error
		cache, cacheErr = GetDpllPins()
		if cacheErr != nil {
			glog.Warningf("PERLA workaround: failed to get pin cache: %v, falling back to serial number approach", cacheErr)
			cache = nil
		}
	}

	if cache != nil {
		// Look for first pin with moduleName == "zl3073x"
		for clockID, pins := range cache.Pins {
			for _, pin := range pins {
				if pin.ModuleName == "zl3073x" {
					glog.Infof("PERLA workaround: Found zl3073x DPLL with clock ID %#x", clockID)
					return clockID, nil
				}
			}
		}
		glog.Warningf("PERLA workaround: No zl3073x DPLL found in pin cache, falling back to serial number approach")
	}

	return 0, fmt.Errorf("no zl3073x DPLL found in pin cache")
}

// Clock ID resolution methods configurable via defaults.yaml clockIdTransformation.method
const (
	ClockIDMethodDirect          = "direct"          // PCI serial number bytes used directly (E810)
	ClockIDMethodEUI64           = "eui64"           // EUI-64 transform of serial number
	ClockIDMethodDevlinkPinChain = "devlinkPinChain" // Trace NIC DPLL pin parent chain (E825/zl3073x)
)

// getClockIDResolutionMethod loads the hardware defaults and returns the configured method.
// Falls back to lspci-based detection when no hwDefPath is provided (legacy compatibility).
func getClockIDResolutionMethod(hwDefPath string, busAddr string) string {
	if hwDefPath != "" {
		spec, err := LoadHardwareDefaults(hwDefPath, nil)
		if err == nil && spec != nil && spec.ClockIDTransformation != nil && spec.ClockIDTransformation.Method != "" {
			return spec.ClockIDTransformation.Method
		}
	}

	// Legacy fallback: detect E825 via lspci when no hwDefPath or no method configured
	lspciOutput, err := commandExecutor.Execute("lspci", "-s", busAddr)
	if err == nil && strings.Contains(lspciOutput, "E825") {
		glog.Infof("Legacy detection: E825 device on %s, using devlinkPinChain method", busAddr)
		return ClockIDMethodDevlinkPinChain
	}

	return ClockIDMethodDirect
}

// GetClockIDFromInterfaceWithCache resolves clock ID with an optional pre-loaded pin cache.
// The resolution method is determined by the hardware definition's clockIdTransformation.method:
//   - "direct" / "eui64": derive clock ID from NIC's PCI serial number (devlink)
//   - "devlinkPinChain": trace NIC's DPLL pin parent chain to external DPLL module
func GetClockIDFromInterfaceWithCache(iface string, hwDefPath string, pinCache *PinCache) (uint64, error) {
	ethtoolOutput, err := commandExecutor.Execute("ethtool", "-i", iface)
	if err != nil {
		return 0, fmt.Errorf("failed to get bus info for interface %s: %w", iface, err)
	}

	busAddr := ""
	for _, line := range strings.Split(ethtoolOutput, "\n") {
		if strings.HasPrefix(line, "bus-info:") {
			busAddr = strings.TrimSpace(strings.TrimPrefix(line, "bus-info:"))
		}
	}
	if busAddr == "" {
		return 0, fmt.Errorf("no bus-info found for interface %s", iface)
	}
	glog.V(4).Infof("ClockID: iface=%s bus=%s hwDef=%s", iface, busAddr, hwDefPath)

	method := getClockIDResolutionMethod(hwDefPath, busAddr)
	glog.Infof("ClockID resolution for %s: method=%s (hwDef=%s)", iface, method, hwDefPath)

	switch method {
	case ClockIDMethodDevlinkPinChain:
		return resolveClockIDViaPinChain(iface, pinCache)
	default:
		return resolveClockIDViaSerialNumber(iface, busAddr, hwDefPath)
	}
}

// resolveClockIDViaPinChain traces the NIC's DPLL pin parent chain to find the
// external DPLL clock ID. Falls back to module-name scan on failure.
func resolveClockIDViaPinChain(iface string, pinCache *PinCache) (uint64, error) {
	clockID, derivErr := getClockIDFromPinParentChain(iface)
	if derivErr == nil {
		return clockID, nil
	}
	glog.Warningf("NIC-to-DPLL derivation failed for %s: %v; falling back to module-name scan", iface, derivErr)

	clockID, err := getPERLAClockIDFromPinCache(pinCache)
	if err != nil {
		return 0, fmt.Errorf("clock ID resolution failed for %s: derivation: %v, fallback: %w", iface, derivErr, err)
	}
	return clockID, nil
}

// resolveClockIDViaSerialNumber derives clock ID from the NIC's PCI serial number
// using the hardware-specific transformer (direct or EUI-64).
func resolveClockIDViaSerialNumber(iface, busAddr, hwDefPath string) (uint64, error) {
	devlinkOutput, err := commandExecutor.Execute("devlink", "dev", "info", fmt.Sprintf("pci/%s", busAddr))
	if err != nil {
		return 0, fmt.Errorf("failed to get devlink info for bus %s: %w", busAddr, err)
	}

	serialNumber := ""
	for _, line := range strings.Split(devlinkOutput, "\n") {
		if strings.Contains(line, "serial_number") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				serialNumber = parts[len(parts)-1]
				break
			}
		}
	}
	if serialNumber == "" {
		return 0, fmt.Errorf("no serial_number found for interface %s (bus: %s)", iface, busAddr)
	}
	glog.V(4).Infof("ClockID: iface=%s bus=%s serial=%s hwDef=%s", iface, busAddr, serialNumber, hwDefPath)

	transformer := getClockIDTransformer(hwDefPath)
	clockID, err := transformer(serialNumber)
	if err != nil {
		return 0, fmt.Errorf("failed to parse serial number %s for interface %s: %w", serialNumber, iface, err)
	}

	glog.Infof("Resolved clock ID %#x for interface %s (bus: %s, serial: %s)", clockID, iface, busAddr, serialNumber)
	return clockID, nil
}

// parseSerialNumberToClockIDDirect uses serial number bytes directly without modification
// This is the method used by Intel E810 and similar hardware
// Serial format: "50-7c-6f-ff-ff-5c-4a-e8"
// Result: 0x507c6fffff5c4ae8 (keeping the ff-ff as-is)
func parseSerialNumberToClockIDDirect(serialNumber string) (uint64, error) {
	// Split by dash and join directly - no transformation needed
	parts := strings.Split(serialNumber, "-")
	if len(parts) != 8 {
		return 0, fmt.Errorf("invalid serial number format for direct transformation: %s (expected 8 bytes)", serialNumber)
	}

	// Join all bytes directly
	clockIDStr := strings.Join(parts, "")
	clockID, err := strconv.ParseUint(clockIDStr, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse clock ID from %s: %w", clockIDStr, err)
	}

	return clockID, nil
}

// parseSerialNumberToClockIDEUI64 converts serial to clock ID using EUI-64 format
// EUI-64 format: remove bytes 3-4 (typically ff-ff), insert ff-fe
// Serial format: "64-4c-36-ff-ff-5c-4a-e8"
// Result: 0x644c36fffe5c4ae8
func parseSerialNumberToClockIDEUI64(serialNumber string) (uint64, error) {
	// Split by dash
	parts := strings.Split(serialNumber, "-")
	if len(parts) < 6 {
		return 0, fmt.Errorf("invalid serial number format: %s (expected at least 6 parts)", serialNumber)
	}

	// Build the clock ID:
	// Take first 3 bytes, add fffe, then remaining bytes (skip indices 3 and 4)
	var clockIDParts []string

	// Add first 3 bytes (indices 0, 1, 2)
	if len(parts) >= 3 {
		clockIDParts = append(clockIDParts, parts[0:3]...)
	}

	// Add the fixed fffe value
	clockIDParts = append(clockIDParts, "ff", "fe")

	// Add remaining bytes (from index 5 onwards, skipping 3 and 4)
	if len(parts) > 5 {
		clockIDParts = append(clockIDParts, parts[5:]...)
	}

	// Join and convert to uint64
	clockIDStr := strings.Join(clockIDParts, "")
	clockID, err := strconv.ParseUint(clockIDStr, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse clock ID from %s: %w", clockIDStr, err)
	}

	return clockID, nil
}

// Count returns the total number of pins in the cache
func (pc *PinCache) Count() int {
	count := 0
	for _, clockPins := range pc.Pins {
		count += len(clockPins)
	}
	return count
}

// GetPin returns the pin info for a specific clock ID and board label
func (pc *PinCache) GetPin(clockID uint64, boardLabel string) (*dpll.PinInfo, bool) {
	if clockPins, exists := pc.Pins[clockID]; exists {
		if pinInfo, found := clockPins[boardLabel]; found {
			return &pinInfo, true
		}
	}
	glog.Infof("Pin cache miss: clockID=%#x boardLabel=%s", clockID, boardLabel)
	return nil, false
}

// getRealDpllPins connects to the real DPLL and returns the DPLL pin cache
func getRealDpllPins() (*PinCache, error) {
	conn, err := dpll.Dial(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to dial DPLL: %v", err)
	}
	//nolint:errcheck
	defer conn.Close()
	dpllPins, err := conn.DumpPinGet()
	if err != nil {
		return nil, fmt.Errorf("failed to dump DPLL pins: %v", err)
	}
	return buildPinCacheFromPins(dpllPins), nil
}

// CreateMockDpllPinsGetter creates a DpllPinsGetter function that returns mock pins
func CreateMockDpllPinsGetter(pins []*dpll.PinInfo, returnError error) DpllPinsGetter {
	return func() (*PinCache, error) {
		if returnError != nil {
			return nil, returnError
		}
		return buildPinCacheFromPins(pins), nil
	}
}

func buildPinCacheFromPins(pins []*dpll.PinInfo) *PinCache {
	cache := &PinCache{
		Pins: make(map[uint64]map[string]dpll.PinInfo),
	}
	for _, pin := range pins {
		if pin == nil {
			continue
		}
		if pin.BoardLabel == "" {
			continue
		}
		if cache.Pins[pin.ClockID] == nil {
			cache.Pins[pin.ClockID] = make(map[string]dpll.PinInfo)
		}
		cache.Pins[pin.ClockID][pin.BoardLabel] = *pin
		glog.Infof("Pin cache add: clock=%#x boardLabel=%s id=%d", pin.ClockID, pin.BoardLabel, pin.ID)
	}
	return cache
}

// PinParentControl represents a DPLL pin control structure
type PinParentControl struct {
	EecPriority    uint8
	PpsPriority    uint8
	EecOutputState uint8
	PpsOutputState uint8
}

// PinControl represents DPLL pin control configuration
type PinControl struct {
	Label         string
	ParentControl PinParentControl
}

// GetPinStateUint32 returns DPLL pin state as a string
func GetPinStateUint32(s string) (uint32, error) {
	stateMap := map[string]uint32{
		"connected":    dpll.PinStateConnected,
		"disconnected": dpll.PinStateDisconnected,
		"selectable":   dpll.PinStateSelectable,
	}
	r, found := stateMap[s]
	if found {
		return r, nil
	}
	return 0, fmt.Errorf("invalid pin state: %s", s)
}

// BatchPinSet applies a batch of DPLL pin commands
func BatchPinSet(commands []dpll.PinParentDeviceCtl) error {
	conn, err := dpll.Dial(nil)
	if err != nil {
		return fmt.Errorf("failed to dial DPLL: %v", err)
	}
	//nolint:errcheck
	defer conn.Close()
	for _, command := range commands {
		glog.Infof("DPLL pin command %s", formatDpllPinCommand(command))
		b, encodeErr := dpll.EncodePinControl(command)
		if encodeErr != nil {
			return encodeErr
		}
		err = conn.SendCommand(dpll.DpllCmdPinSet, b)
		if err != nil {
			glog.Error("failed to send pin command: ", err)
			return err
		}
		info, getErr := conn.DoPinGet(dpll.DoPinGetRequest{ID: command.ID})
		if getErr != nil {
			glog.Error("failed to get pin: ", getErr)
			//TODO: handle properly after RHEL-137801 is fixed
			return nil
		}
		reply, replyErr := dpll.GetPinInfoHR(info, time.Now())
		if replyErr != nil {
			glog.Error("failed to convert pin reply to human readable: ", replyErr)
			return replyErr
		}
		glog.Info("pin reply: ", string(reply))
	}
	return nil
}

// formatDpllPinCommand returns a human-readable one-line description of a DPLL pin command.
func formatDpllPinCommand(command dpll.PinParentDeviceCtl) string {
	descParts := make([]string, 0, 6)
	descParts = append(descParts, fmt.Sprintf("id=%d", command.ID))
	if command.Frequency != nil {
		descParts = append(descParts, fmt.Sprintf("frequency=%d", *command.Frequency))
	}
	if command.EsyncFrequency != nil {
		descParts = append(descParts, fmt.Sprintf("esync=%d", *command.EsyncFrequency))
	}
	if command.PhaseAdjust != nil {
		descParts = append(descParts, fmt.Sprintf("phaseAdjust=%d", *command.PhaseAdjust))
	}
	if len(command.PinParentCtl) > 0 {
		parentSummaries := make([]string, 0, len(command.PinParentCtl))
		for _, pc := range command.PinParentCtl {
			fields := make([]string, 0, 4)
			fields = append(fields, fmt.Sprintf("id=%d", pc.PinParentID))
			if pc.Direction != nil {
				fields = append(fields, fmt.Sprintf("dir=%s", dpll.GetPinDirection(*pc.Direction)))
			}
			if pc.State != nil {
				fields = append(fields, fmt.Sprintf("state=%s", dpll.GetPinState(*pc.State)))
			}
			if pc.Prio != nil {
				fields = append(fields, fmt.Sprintf("prio=%d", *pc.Prio))
			}
			parentSummaries = append(parentSummaries, strings.Join(fields, " "))
		}
		descParts = append(descParts, fmt.Sprintf("parents=[%s]", strings.Join(parentSummaries, "; ")))
	}
	if len(descParts) == 1 { // only id present
		descParts = append(descParts, "no-op")
	}
	return strings.Join(descParts, " ")
}

// NOTE: SetupMockDpllPinsForTests has been moved to utils_test.go to avoid hardcoded data in production code.

// SetupMockDpllPinsForTestsWithData sets up mock DPLL pins with custom data
func SetupMockDpllPinsForTestsWithData(pins []*dpll.PinInfo) {
	mockGetter := CreateMockDpllPinsGetter(pins, nil)
	SetDpllPinsGetter(mockGetter)
}

// SetupMockDpllPinsForTestsWithError sets up mock DPLL pins that returns an error
func SetupMockDpllPinsForTestsWithError(err error) {
	mockGetter := CreateMockDpllPinsGetter(nil, err)
	SetDpllPinsGetter(mockGetter)
}

// TeardownMockDpllPinsForTests cleans up DPLL pin mocks after testing
func TeardownMockDpllPinsForTests() {
	ResetDpllPinsGetter()
}

// PtpDeviceResolver is a function type for resolving PTP device paths
type PtpDeviceResolver func(interfacePath string) ([]string, error)

// defaultResolveSysFSPtpDevice is the default implementation that reads from the real file system
func defaultResolveSysFSPtpDevice(interfacePath string) ([]string, error) {
	// If path doesn't contain "ptp*" placeholder, return as-is
	if !strings.Contains(interfacePath, "ptp*") {
		return []string{interfacePath}, nil
	}

	// Extract the directory path and filename
	pathParts := strings.Split(interfacePath, "ptp*")
	if len(pathParts) != 2 {
		return nil, fmt.Errorf("invalid ptp* pattern in path: %s", interfacePath)
	}

	ptpDir := filepath.Dir(pathParts[0] + "ptp0") // Use ptp0 as template to get the directory
	filename := pathParts[1]

	// Read the PTP devices directory
	entries, err := os.ReadDir(ptpDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read ptp devices directory %s: %w", ptpDir, err)
	}

	var resolvedPaths []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "ptp") {
			// Construct the full path
			fullPath := filepath.Join(ptpDir, entry.Name()) + filename

			// Check if the target file exists and is writable
			if info, statErr := os.Stat(fullPath); statErr == nil && !info.IsDir() {
				// Try to open the file for writing to check if it's writable
				if file, openErr := os.OpenFile(fullPath, os.O_WRONLY, 0); openErr == nil {
					_ = file.Close()
					resolvedPaths = append(resolvedPaths, fullPath)
				}
			}
		}
	}

	if len(resolvedPaths) == 0 {
		return nil, fmt.Errorf("no writable files found for path %s", interfacePath)
	}

	return resolvedPaths, nil
}

// Global variables for PTP device resolution mocking
var (
	defaultPtpDeviceResolver PtpDeviceResolver = defaultResolveSysFSPtpDevice
	ptpDeviceResolver                          = defaultPtpDeviceResolver
)

// SetPtpDeviceResolver allows injection of a mock PTP device resolver for testing
func SetPtpDeviceResolver(resolver PtpDeviceResolver) {
	ptpDeviceResolver = resolver
}

// ResetPtpDeviceResolver resets the PTP device resolver to the default implementation
func ResetPtpDeviceResolver() {
	ptpDeviceResolver = defaultPtpDeviceResolver
}

// CreateMockPtpDeviceResolver creates a mock PTP device resolver
func CreateMockPtpDeviceResolver(mockDevices map[string][]string, returnError error) PtpDeviceResolver {
	return func(interfacePath string) ([]string, error) {
		if returnError != nil {
			return nil, returnError
		}

		if devices, exists := mockDevices[interfacePath]; exists {
			return devices, nil
		}

		// If no specific mock is provided, try to extract a pattern and return mock devices
		if strings.Contains(interfacePath, "ptp*") {
			// Replace ptp* with mock devices
			var result []string
			for i := 0; i < 2; i++ { // Default to 2 mock devices
				mockPath := strings.Replace(interfacePath, "ptp*", fmt.Sprintf("ptp%d", i), 1)
				result = append(result, mockPath)
			}
			return result, nil
		}

		return []string{interfacePath}, nil
	}
}

// SetupMockPtpDeviceResolver sets up a default mock PTP device resolver for tests
func SetupMockPtpDeviceResolver() {
	mockDevices := make(map[string][]string)
	mockResolver := CreateMockPtpDeviceResolver(mockDevices, nil)
	SetPtpDeviceResolver(mockResolver)
}

// SetupMockPtpDeviceResolverWithDevices sets up a mock PTP device resolver with specific devices
func SetupMockPtpDeviceResolverWithDevices(mockDevices map[string][]string) {
	mockResolver := CreateMockPtpDeviceResolver(mockDevices, nil)
	SetPtpDeviceResolver(mockResolver)
}

// SetupMockPtpDeviceResolverWithError sets up a mock PTP device resolver that returns an error
func SetupMockPtpDeviceResolverWithError(err error) {
	mockResolver := CreateMockPtpDeviceResolver(nil, err)
	SetPtpDeviceResolver(mockResolver)
}

// TeardownMockPtpDeviceResolver resets the PTP device resolver to the default implementation
func TeardownMockPtpDeviceResolver() {
	ResetPtpDeviceResolver()
}
