package network

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/golang/glog"
	ptpv1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v1"
	ptpv2alpha1 "github.com/k8snetworkplumbingwg/ptp-operator/api/v2alpha1"
)

var (
	dpllKeyRegex    = regexp.MustCompile(`^dpll\.(.+)\.(ignore|flags)$`)
	clockIDKeyRegex = regexp.MustCompile(`^(clockId|clockIdFormat)\[(.+)\]$`)
)

// InterfaceResolver maps configured interface names to actual system interface
// names, handling the RHEL 10 npN suffix change transparently.
type InterfaceResolver struct {
	knownInterfaces map[string]bool
	remapCache      map[string]string
}

// NewInterfaceResolver creates a resolver that scans /sys/class/net/ for current interfaces.
func NewInterfaceResolver() *InterfaceResolver {
	r := &InterfaceResolver{
		knownInterfaces: make(map[string]bool),
		remapCache:      make(map[string]string),
	}
	if err := r.Refresh(); err != nil {
		glog.Warningf("InterfaceResolver: initial scan of /sys/class/net failed: %v", err)
	}
	return r
}

// NewInterfaceResolverWithInterfaces creates a resolver pre-populated with the
// given interface names. Intended for unit tests.
func NewInterfaceResolverWithInterfaces(interfaces []string) *InterfaceResolver {
	r := &InterfaceResolver{
		knownInterfaces: make(map[string]bool, len(interfaces)),
		remapCache:      make(map[string]string),
	}
	for _, iface := range interfaces {
		r.knownInterfaces[iface] = true
	}
	return r
}

// npSuffix returns the integer N from an interface name ending in "npN".
// Returns -1 if no suffix is found.
func npSuffix(iface string) int {
	idx := strings.LastIndex(iface, "np")
	if idx < 0 || idx+2 >= len(iface) {
		return -1
	}
	n, err := strconv.Atoi(iface[idx+2:])
	if err != nil {
		return -1
	}
	return n
}

// Refresh re-scans /sys/class/net/ and clears the remap cache.
func (r *InterfaceResolver) Refresh() error {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return fmt.Errorf("failed to read /sys/class/net: %w", err)
	}
	r.knownInterfaces = make(map[string]bool, len(entries))
	for _, entry := range entries {
		r.knownInterfaces[entry.Name()] = true
	}
	r.remapCache = make(map[string]string)
	return nil
}

// Resolve returns the actual system interface name for a configured name.
// If the configured name exists, it is returned unchanged.
// Otherwise the resolver tries appending npN (N=0..7).
func (r *InterfaceResolver) Resolve(configuredName string) (string, bool) {
	if configuredName == "" {
		return configuredName, false
	}

	if cached, ok := r.remapCache[configuredName]; ok {
		return cached, cached != configuredName
	}

	if r.knownInterfaces[configuredName] {
		r.remapCache[configuredName] = configuredName
		return configuredName, false
	}

	// Match any interface named configuredName + "np" + digits
	npPattern := regexp.MustCompile(`^` + regexp.QuoteMeta(configuredName) + `np\d+$`)
	var candidates []string
	for iface := range r.knownInterfaces {
		if npPattern.MatchString(iface) {
			candidates = append(candidates, iface)
		}
	}
	// Map iteration order is randomized; sort by numeric npN suffix so the
	// lowest-numbered interface (e.g. np0 before np1/np10) is always chosen.
	sort.Slice(candidates, func(i, j int) bool {
		return npSuffix(candidates[i]) < npSuffix(candidates[j])
	})

	if len(candidates) == 1 {
		resolved := candidates[0]
		glog.Infof("Interface name resolved: %q -> %q (RHEL 10 npN suffix detected)", configuredName, resolved)
		r.remapCache[configuredName] = resolved
		return resolved, true
	}

	if len(candidates) > 1 {
		resolved := candidates[0]
		glog.Warningf("Multiple npN candidates found for %q: %v; using lowest-numbered match %q",
			configuredName, candidates, resolved)
		r.remapCache[configuredName] = resolved
		return resolved, true
	}

	glog.Warningf("Interface %q not found on system and no npN variant exists", configuredName)
	r.remapCache[configuredName] = configuredName
	return configuredName, false
}

// ResolveProfileInterfaces resolves all interface name references in a PtpProfile.
func (r *InterfaceResolver) ResolveProfileInterfaces(profile *ptpv1.PtpProfile) {
	profileName := "<unknown>"
	if profile.Name != nil {
		profileName = *profile.Name
	}

	if profile.Interface != nil && *profile.Interface != "" {
		if resolved, remapped := r.Resolve(*profile.Interface); remapped {
			glog.Infof("Profile %s: Interface field remapped %q -> %q", profileName, *profile.Interface, resolved)
			*profile.Interface = resolved
		}
	}

	// Config section headers ([interface]) are resolved by Ptp4lConf.ResolveInterfaceNames
	// after parsing, inside applyNodePtpProfile — not here.

	if profile.PtpSettings != nil {
		resolvePtpSettings(r, profile.PtpSettings, profileName)
	}

	resolvePluginDevices(r, profile, profileName)
}

func resolvePtpSettings(r *InterfaceResolver, settings map[string]string, profileName string) {
	// Resolve simple value settings
	for _, key := range []string{"leadingInterface"} {
		if val, ok := settings[key]; ok && val != "" {
			if resolved, remapped := r.Resolve(val); remapped {
				glog.Infof("Profile %s: PtpSettings[%s] remapped %q -> %q", profileName, key, val, resolved)
				settings[key] = resolved
			}
		}
	}

	// upstreamPort can be comma-separated
	if val, ok := settings["upstreamPort"]; ok && val != "" {
		ports := strings.Split(val, ",")
		changed := false
		for i, port := range ports {
			port = strings.TrimSpace(port)
			if resolved, remapped := r.Resolve(port); remapped {
				glog.Infof("Profile %s: PtpSettings[upstreamPort] port %q -> %q", profileName, port, resolved)
				ports[i] = resolved
				changed = true
			}
		}
		if changed {
			settings["upstreamPort"] = strings.Join(ports, ",")
		}
	}

	// Resolve keys that embed interface names
	renames := make(map[string]string)
	for key := range settings {
		newKey := resolveDpllKey(r, key)
		if newKey == "" {
			newKey = resolveClockIDKey(r, key)
		}
		if newKey != "" {
			renames[key] = newKey
			glog.Infof("Profile %s: PtpSettings key %q -> %q", profileName, key, newKey)
		}
	}
	for oldKey, newKey := range renames {
		if _, exists := settings[newKey]; exists {
			glog.Warningf("Profile %s: PtpSettings key rename %q -> %q skipped: target key already exists",
				profileName, oldKey, newKey)
			continue
		}
		settings[newKey] = settings[oldKey]
		delete(settings, oldKey)
	}
}

func resolveDpllKey(r *InterfaceResolver, key string) string {
	m := dpllKeyRegex.FindStringSubmatch(key)
	if m == nil {
		return ""
	}
	resolved, remapped := r.Resolve(m[1])
	if !remapped {
		return ""
	}
	return fmt.Sprintf("dpll.%s.%s", resolved, m[2])
}

func resolveClockIDKey(r *InterfaceResolver, key string) string {
	m := clockIDKeyRegex.FindStringSubmatch(key)
	if m == nil {
		return ""
	}
	resolved, remapped := r.Resolve(m[2])
	if !remapped {
		return ""
	}
	return fmt.Sprintf("%s[%s]", m[1], resolved)
}

func resolvePluginDevices(r *InterfaceResolver, profile *ptpv1.PtpProfile, profileName string) {
	if profile.Plugins == nil {
		return
	}

	for pluginName, opts := range profile.Plugins {
		if opts == nil {
			continue
		}
		var rawOpts map[string]any
		if err := json.Unmarshal(opts.Raw, &rawOpts); err != nil {
			continue
		}

		changed := false

		// Resolve "devices" array
		if devices, ok := rawOpts["devices"].([]any); ok {
			for i, d := range devices {
				name, isStr := d.(string)
				if !isStr {
					glog.Warningf("Profile %s: plugin %s devices[%d] is not a string, skipping", profileName, pluginName, i)
					continue
				}
				if resolved, remapped := r.Resolve(name); remapped {
					devices[i] = resolved
					changed = true
					glog.Infof("Profile %s: plugin %s device %q -> %q", profileName, pluginName, name, resolved)
				}
			}
		}

		// Resolve map keys in pins, frequencies, phaseOffsetPins
		for _, mapKey := range []string{"pins", "frequencies", "phaseOffsetPins"} {
			if m, ok := rawOpts[mapKey].(map[string]any); ok {
				renames := make(map[string]string)
				for deviceName := range m {
					if resolved, remapped := r.Resolve(deviceName); remapped {
						renames[deviceName] = resolved
						changed = true
						glog.Infof("Profile %s: plugin %s %s key %q -> %q", profileName, pluginName, mapKey, deviceName, resolved)
					}
				}
				for oldKey, newKey := range renames {
					if _, exists := m[newKey]; exists {
						glog.Warningf("Profile %s: plugin %s %s key rename %q -> %q skipped: target key already exists",
							profileName, pluginName, mapKey, oldKey, newKey)
						continue
					}
					m[newKey] = m[oldKey]
					delete(m, oldKey)
				}
			}
		}

		// Resolve interface names in the "interconnections" array. Each element
		// is an object with an "id" field and an optional comma-separated
		// "upstreamPort" field, both of which reference interface names.
		if interconnections, ok := rawOpts["interconnections"].([]any); ok {
			for i, ic := range interconnections {
				elem, isMap := ic.(map[string]any)
				if !isMap {
					glog.Warningf("Profile %s: plugin %s interconnections[%d] is not an object, skipping", profileName, pluginName, i)
					continue
				}
				if id, isStr := elem["id"].(string); isStr && id != "" {
					if resolved, remapped := r.Resolve(id); remapped {
						elem["id"] = resolved
						changed = true
						glog.Infof("Profile %s: plugin %s interconnections[%d] id %q -> %q", profileName, pluginName, i, id, resolved)
					}
				}
				// upstreamPort can be comma-separated, mirroring PtpSettings["upstreamPort"]
				if val, isStr := elem["upstreamPort"].(string); isStr && val != "" {
					ports := strings.Split(val, ",")
					portsChanged := false
					for pi, port := range ports {
						port = strings.TrimSpace(port)
						if resolved, remapped := r.Resolve(port); remapped {
							glog.Infof("Profile %s: plugin %s interconnections[%d] upstreamPort port %q -> %q", profileName, pluginName, i, port, resolved)
							ports[pi] = resolved
							portsChanged = true
						}
					}
					if portsChanged {
						elem["upstreamPort"] = strings.Join(ports, ",")
						changed = true
					}
				}
			}
		}

		if changed {
			newRaw, err := json.Marshal(rawOpts)
			if err != nil {
				glog.Errorf("Profile %s: failed to re-marshal plugin %s opts: %v", profileName, pluginName, err)
				continue
			}
			opts.Raw = newRaw
		}
	}
}

// ResolveHardwareConfigInterfaces resolves interface names in HardwareConfig CRs.
func (r *InterfaceResolver) ResolveHardwareConfigInterfaces(hwConfigs []ptpv2alpha1.HardwareConfig) {
	for i := range hwConfigs {
		cc := hwConfigs[i].Spec.Profile.ClockChain
		if cc == nil {
			continue
		}
		configName := hwConfigs[i].Name

		// Resolve Structure subsystem interface names
		for si := range cc.Structure {
			sub := &cc.Structure[si]
			if sub.DPLL.NetworkInterface != "" {
				if resolved, remapped := r.Resolve(sub.DPLL.NetworkInterface); remapped {
					glog.Infof("HardwareConfig %s: subsystem %s DPLL.NetworkInterface %q -> %q",
						configName, sub.Name, sub.DPLL.NetworkInterface, resolved)
					sub.DPLL.NetworkInterface = resolved
				}
			}
			for ei := range sub.Ethernet {
				for pi := range sub.Ethernet[ei].Ports {
					if resolved, remapped := r.Resolve(sub.Ethernet[ei].Ports[pi]); remapped {
						glog.Infof("HardwareConfig %s: subsystem %s Ethernet port %q -> %q",
							configName, sub.Name, sub.Ethernet[ei].Ports[pi], resolved)
						sub.Ethernet[ei].Ports[pi] = resolved
					}
				}
			}
		}

		// Resolve Behavior source PTPTimeReceivers
		if cc.Behavior != nil {
			for si := range cc.Behavior.Sources {
				src := &cc.Behavior.Sources[si]
				for pi := range src.PTPTimeReceivers {
					if resolved, remapped := r.Resolve(src.PTPTimeReceivers[pi]); remapped {
						glog.Infof("HardwareConfig %s: source %s PTPTimeReceiver %q -> %q",
							configName, src.Name, src.PTPTimeReceivers[pi], resolved)
						src.PTPTimeReceivers[pi] = resolved
					}
				}
			}
		}
	}
}
