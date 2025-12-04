# Intel E810 Hardware Defaults

This directory contains hardware-specific configuration for Intel E810 NICs.

## Pin eSync Commands

The `pinEsyncCommands` section defines command sequences for configuring pins with eSync (embedded synchronization).

### Command Structure

Each command can have:
- `type`: Command type (currently only "DPLLWrite" is supported)
- `description`: Human-readable description of what the command does
- `arguments`: List of arguments to set in this command (minimum 1 if not using pinParentDevices)
- `pinParentDevices`: Parent device state configurations

### Supported Arguments

- `frequency`: Transfer frequency (carrier frequency)
- `eSyncFrequency`: Embedded synchronization frequency

### Examples

#### Single Argument per Command
```yaml
pinEsyncCommands:
  outputs:
    - description: set Frequency
      type: DPLLWrite
      arguments:
        - frequency
    - description: set eSync frequency
      type: DPLLWrite
      arguments:
        - eSyncFrequency
```

#### Multiple Arguments per Command
```yaml
pinEsyncCommands:
  outputs:
    - description: set frequency and eSync frequency
      type: DPLLWrite
      arguments:
        - frequency
        - eSyncFrequency
    - description: re-enable output
      type: DPLLWrite
      pinParentDevices:
        - parentDevice: EEC
          state: connected
        - parentDevice: PPS
          state: connected
```

### Command Sequence

For the E810, the eSync output configuration sequence is:
1. Set the frequency
2. Set the eSyncFrequency
3. Re-enable the output by setting parent devices to "connected"

