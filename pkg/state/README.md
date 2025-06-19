# Shared state

## Overview

The parser package consists of several components:

- **State Management**: Maintain state information about PTP configuration

## Usage

### Working with State

The parser maintains state information about the PTP configuration. You can access this information using the state object:

```go
// Set master interface
err := state.SetMasterOffsetIface("ptp4l.0.config", "ens5f0")

// Set slave interface
err := state.SetSlaveIface("ptp4l.0.config", "ens5f1")

// Get master interface
masterIface := state.GetMasterInterface("ptp4l.0.config")

// Get slave interface
slaveIface := state.GetSlaveIface("ptp4l.0.config")

// Check if interface is faulty
isFaulty := state.IsFaultySlaveIface("ptp4l.0.config", "ens5f1")

// Get master interface by alias
masterIface, err := state.GetMasterInterfaceByAlias("ptp4l.0.config", "ens5f0")

// Get alias by name
iface, err := state.GetAliasByName("ptp4l.0.config", "ens5f0")
```

## Contributing

When adding new features or modifying existing ones:

1. Add appropriate tests
2. Update documentation
3. Follow the existing code style
4. Handle all error cases
5. Maintain backward compatibility
