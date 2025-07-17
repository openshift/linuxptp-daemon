// Package state provides state state management for PTP (Precision Time Protocol) interfaces
// and related functionality. It handles synchronization of interface states, master-slave
// relationships, and offset tracking.
package state

import (
	"fmt"
	"sync"

	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/constants"
	"github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/utils"
)

// PtpInterface represents a PTP interface with its name and alias
type PtpInterface struct {
	Name  string
	Alias string
}

type masterOffsetInterface struct { // by slave iface with masked index
	mu    sync.RWMutex
	iface map[string]PtpInterface
}
type slaveInterface struct { // current slave iface name
	mu   sync.RWMutex
	name map[string]string
}

type masterOffsetSourceProcess struct { // current slave iface name
	mu   sync.RWMutex
	name map[string]string
}

// SharedState manages the state state between different PTP components.
// It handles synchronization of interface states, master-slave relationships,
// and offset tracking across the PTP daemon.
type SharedState struct {
	masterOffsetIface  *masterOffsetInterface
	slaveIface         *slaveInterface
	masterOffsetSource *masterOffsetSourceProcess
}

// NewSharedState creates a new SharedState.
func NewSharedState() *SharedState {
	return &SharedState{
		masterOffsetIface: &masterOffsetInterface{
			iface: map[string]PtpInterface{},
		},
		slaveIface: &slaveInterface{
			name: map[string]string{},
		},
		masterOffsetSource: &masterOffsetSourceProcess{
			name: map[string]string{},
		},
	}
}

// --- Master Offset Iface Methods ---

// DeleteMasterOffsetIface deletes the master offset interface for the given config name.
func (s *SharedState) DeleteMasterOffsetIface(configName string) {
	if s.masterOffsetIface == nil {
		return // avoid panic
	}
	s.masterOffsetIface.mu.Lock()
	defer s.masterOffsetIface.mu.Unlock()
	delete(s.masterOffsetIface.iface, configName)
}

// --- Slave Iface Methods ---

// GetSlaveIface returns the slave interface for the given config name.
func (s *SharedState) GetSlaveIface(configName string) string {
	if s.slaveIface == nil {
		return " " // avoid panic
	}
	s.slaveIface.mu.RLock()
	defer s.slaveIface.mu.RUnlock()
	return s.slaveIface.name[configName]
}

// DeleteSlaveIface deletes the slave interface for the given config name.
func (s *SharedState) DeleteSlaveIface(configName string) {
	if s.slaveIface == nil {
		return // avoid panic
	}
	s.slaveIface.mu.Lock()
	defer s.slaveIface.mu.Unlock()
	delete(s.slaveIface.name, configName)
}

// GetMasterInterface returns the master interface for the given config name.
// Returns an empty PtpInterface if the interface is not found.
func (s *SharedState) GetMasterInterface(configName string) PtpInterface {
	if s.masterOffsetIface == nil {
		return PtpInterface{
			Name:  "",
			Alias: "",
		} // avoid panic
	}
	s.masterOffsetIface.mu.RLock()
	defer s.masterOffsetIface.mu.RUnlock()
	if mIface, found := s.masterOffsetIface.iface[configName]; found {
		return mIface
	}
	return PtpInterface{
		Name:  "",
		Alias: "",
	}
}

// GetMasterInterfaceByAlias returns the master interface for the given config name and alias.
// Returns an error if the interface is not found.
func (s *SharedState) GetMasterInterfaceByAlias(configName string, alias string) (PtpInterface, error) {
	if s.masterOffsetIface == nil {
		return PtpInterface{}, fmt.Errorf("master interface is nil")
	}
	s.masterOffsetIface.mu.RLock()
	defer s.masterOffsetIface.mu.RUnlock()
	if mIface, found := s.masterOffsetIface.iface[configName]; found {
		if mIface.Alias == alias {
			return mIface, nil
		}
	}
	return PtpInterface{}, fmt.Errorf("master interface not found for config %s and alias %s", configName, alias)
}

// GetAliasByName returns the interface alias for the given config name and interface name.
// Returns an error if the interface is not found.
func (s *SharedState) GetAliasByName(configName string, name string) (PtpInterface, error) {
	if name == "CLOCK_REALTIME" || name == "master" {
		return PtpInterface{
			Name:  name,
			Alias: name,
		}, nil
	}
	if s.masterOffsetIface == nil {
		return PtpInterface{}, fmt.Errorf("master interface is nil")
	}
	s.masterOffsetIface.mu.RLock()
	defer s.masterOffsetIface.mu.RUnlock()
	if mIface, found := s.masterOffsetIface.iface[configName]; found {
		if mIface.Name == name {
			return mIface, nil
		}
	}
	return PtpInterface{}, fmt.Errorf("interface not found for config %s and name %s", configName, name)
}

// SetMasterOffsetIface sets the master interface for the given config name.
// Returns an error if the config name is empty.
func (s *SharedState) SetMasterOffsetIface(configName string, value string) error {
	if configName == "" {
		return fmt.Errorf("config name cannot be empty")
	}
	if s.masterOffsetIface == nil {
		return fmt.Errorf("master interface is nil")
	}
	s.masterOffsetIface.mu.Lock()
	defer s.masterOffsetIface.mu.Unlock()
	s.masterOffsetIface.iface[configName] = PtpInterface{
		Name:  value,
		Alias: utils.GetAlias(value),
	}
	return nil
}

// SetSlaveIface sets the slave interface for the given config name.
// Returns an error if the config name is empty.
func (s *SharedState) SetSlaveIface(configName string, value string) error {
	if configName == "" {
		return fmt.Errorf("config name cannot be empty")
	}
	if s.slaveIface == nil {
		return fmt.Errorf("slave interface is nil")
	}
	s.slaveIface.mu.Lock()
	defer s.slaveIface.mu.Unlock()
	s.slaveIface.name[configName] = value
	return nil
}

// IsFaultySlaveIface checks if the given interface is faulty for the config name.
// Returns true if the interface is faulty, false otherwise.
func (s *SharedState) IsFaultySlaveIface(configName string, iface string) bool {
	if s.slaveIface == nil {
		return false
	}
	s.slaveIface.mu.RLock()
	defer s.slaveIface.mu.RUnlock()

	if si, found := s.slaveIface.name[configName]; found {
		return si == iface
	}
	return false
}

// GetMasterOffsetSource returns the master offset source for the given config name.
// Returns the default value if the source is not found.
func (s *SharedState) GetMasterOffsetSource(configName string) string {
	if s.masterOffsetIface == nil {
		return "uninitialized" // avoid panic
	}
	s.masterOffsetSource.mu.RLock()
	defer s.masterOffsetSource.mu.RUnlock()
	if source, found := s.masterOffsetSource.name[configName]; found {
		return source
	}
	return constants.PTP4L // default is ptp4l
}

// SetMasterOffsetSource sets the master offset source for the given config name.
// Returns an error if the config name is empty.
func (s *SharedState) SetMasterOffsetSource(configName string, value string) error {
	if configName == "" {
		return fmt.Errorf("config name cannot be empty")
	}
	if s.masterOffsetIface == nil {
		return fmt.Errorf("master interface is nil")
	}
	s.masterOffsetSource.mu.Lock()
	defer s.masterOffsetSource.mu.Unlock()
	s.masterOffsetSource.name[configName] = value
	return nil
}
