package alias

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/golang/glog"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

type store struct {
	sync.RWMutex
	interfacesByPhc map[string][]string
	aliases         map[string]string
}

func (m *store) addInterface(phc string, ifName string) {
	m.Lock()
	defer m.Unlock()
	m.interfacesByPhc[phc] = append(m.interfacesByPhc[phc], ifName)
}

// calculateAliases calculates and sets the aliases.
//
// Expects a map of phcID: [ifname, ...].
// If a unique and singular alias is not found
// for a group or set of groups it will log a warning and
// fall back to the interface name
func (m *store) calculateAliases() {
	m.Lock()
	defer m.Unlock()

	errs := make([]error, 0)

	// storeInstance.aliases = make(map[string]string)

	seenAliases := make(map[string][]string)
	for phcID, group := range m.interfacesByPhc {
		ungroup := false

		// Check groups for any non matching alias
		firstIfAlias := utils.GetAliasValue(group[0])
		m.aliases[group[0]] = firstIfAlias
		for _, i := range group[1:] {
			alias := utils.GetAliasValue(i)
			m.aliases[i] = alias

			if firstIfAlias != alias {
				ungroup = true
				errs = append(errs, fmt.Errorf(
					"one or more interfaces in group ['%s'] does not alias to a common value, falling back to interface names. %s",
					strings.Join(group, "', '"),
					helpText,
				))
				break
			}
		}

		// If non-matching alias is found, do not alias any of the group
		if ungroup {
			for _, i := range group {
				m.aliases[i] = i
			}
		} else {
			seenAliases[firstIfAlias] = append(seenAliases[firstIfAlias], phcID)
		}
	}

	// Check if PHCs have matching aliases
	for _, phcIDs := range seenAliases {
		if len(phcIDs) > 1 {
			names := make([]string, 0)
			for _, pid := range phcIDs {
				if group, ok := m.interfacesByPhc[pid]; ok {
					names = append(names, group...)
					for _, i := range group {
						m.aliases[i] = i
					}
				}
			}
			errs = append(errs, fmt.Errorf(
				"one or more PHC IDs have the same alias ['%s'] falling back to interface names. %s",
				strings.Join(names, "', '"),
				helpText,
			))
		}
	}

	if len(errs) > 0 {
		glog.Warning(errors.Join(errs...))
	}
}

func (m *store) getAlias(ifName string) string {
	m.RLock()
	defer m.RUnlock()
	if v, ok := m.aliases[ifName]; ok {
		return v
	}
	return ifName
}

func (m *store) setAlias(ifName, alias string) {
	m.Lock()
	defer m.Unlock()
	m.aliases[ifName] = alias
}

func (m *store) clear() {
	m.Lock()
	defer m.Unlock()
	m.aliases = make(map[string]string)
	m.interfacesByPhc = make(map[string][]string)
}

func (m *store) getAllAliases() map[string]string {
	m.RLock()
	defer m.RUnlock()
	result := make(map[string]string, len(m.aliases))
	for k, v := range m.aliases {
		result[k] = v
	}
	return result
}

var storeInstance = store{aliases: make(map[string]string), interfacesByPhc: make(map[string][]string)}
