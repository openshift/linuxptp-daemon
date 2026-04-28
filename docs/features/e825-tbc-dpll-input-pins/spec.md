# Feature Spec: E825 T-BC DPLL Input Pin Enablement

## Status: Implemented

## Problem Statement

The E825 plugin does not automatically enable DPLL input pins when configured as a Telecom Boundary Clock (T-BC). Currently, the plugin only manages GNSS pins (disabling for T-BC, enabling for T-GM) and PHC SDP pins (SDP0/SDP2 for the PHY-to-DPLL sync path). The DPLL input pins REF0P and REF0N, which receive SDP timing signals from the NAC, remain in `disconnected` state after system reset. Without enabling these pins, the DPLL cannot lock to the upstream PTP timing signal forwarded through the SDP path.

## Current Behavior Analysis

### What the E825 plugin does today for T-BC (`OnPTPConfigChangeE825`):
1. Sets `e825Opts.Gnss.Disabled = true` (disables GNSS input)
2. Calls `setupGnss()` which finds GNSS-type pins (`PinTypeGNSS`) and sets them to `disconnected`
3. Validates `upstreamPort` and `leadingInterface` are configured
4. Applies `bcDpllPinReset` to disable SDP0 and SDP2 PHC pins on the leading interface

### What the E825 plugin does on sync events (`AfterRunPTPCommandE825`):
- `tbc-ho-exit` (sync achieved): Enables SDP0/SDP2 as outputs via `bcDpllPinSetup` and configures frequencies via `bcDpllPeriods`
- `tbc-ho-entry` (sync lost): Disables SDP0/SDP2 via `bcDpllPinReset`

### What is missing:
The DPLL input pins REF0P and REF0N are never configured. They remain `disconnected` out of reset on all known platforms.

## Platform Evidence

### Dell Platform — DPLL input pins out of reset:
| ID | Board Label | Package Label | EEC Prio | PPS Prio | EEC State | PPS State |
|----|---|---|---|---|---|---|
| 0 | ETH01_SDP_TIMESYNC_2 | REF0P | 14 | 6 | disconnected | disconnected |
| 1 | ETH01_SDP_TIMESYNC_0 | REF0N | 14 | 7 | disconnected | disconnected |
| 7 | GNSS_1PPS_IN | REF4P | 0 | 0 | connected | connected |

### HPE Platform — DPLL input pins out of reset:
| ID | Board Label | Package Label | EEC Prio | PPS Prio | EEC State | PPS State |
|----|---|---|---|---|---|---|
| 0 | 1PPS_IN1 | REF0P | 14 | 6 | disconnected | disconnected |
| 1 | 1PPS_IN0 | REF0N | 14 | 7 | disconnected | disconnected |
| 7 | GNSS_1PPS_IN | REF4P | 0 | 0 | connected | connected |

### Key Observation:
- **Board labels differ** between platforms (Dell: `ETH01_SDP_TIMESYNC_2`/`ETH01_SDP_TIMESYNC_0`, HPE: `1PPS_IN1`/`1PPS_IN0`)
- **Package labels are identical** across platforms: `REF0P` and `REF0N`
- Both platforms show these pins as `disconnected` with high (disabled) priority values out of reset
- The GNSS pin (REF4P / `GNSS_1PPS_IN`) is `connected` out of reset — this is what `setupGnss` currently toggles

## Requirements

### REQ-1: Enable REF0P and REF0N DPLL input pins for T-BC

When the E825 plugin detects a T-BC configuration (`clockType == "T-BC"`), it must set the DPLL input pins with package labels `REF0P` and `REF0N` to `selectable` state on the **PPS parent device only**.

#### REQ-1.1: Pin identification by package label
Pins must be identified by their `PackageLabel` field (not `BoardLabel`), since board labels vary across platforms while package labels are consistent.

#### REQ-1.2: Target pins
The following DPLL input pins must be set to `selectable`:
- `REF0P` — PPS parent device state: `selectable`
- `REF0N` — PPS parent device state: `selectable`

#### REQ-1.3: PPS parent only
Only the PPS parent device state should be changed to `selectable`. The PPS parent is identified dynamically by looking up the DPLL device matching the pin's `ParentID` and `ClockID` with `GetDpllType(dev.Type) == "pps"`. The EEC parent device state should remain unchanged (`disconnected`).

#### REQ-1.4: Timing of pin enablement
The DPLL input pins must be enabled during `OnPTPConfigChangeE825`, alongside the existing GNSS pin configuration and `bcDpllPinReset` application. This ensures the DPLL input path is ready before PTP synchronization begins.

#### REQ-1.5: Pin direction and type filter
Only pins matching ALL of the following criteria should be affected:
- `PackageLabel` matches `REF0P` or `REF0N`
- Pin direction is `input` (on at least the PPS parent device)
- Pin has `PinCapState` capability (state can be changed)

#### REQ-1.6: Error handling
- If REF0P or REF0N pins cannot be found, log a warning but do not fail the plugin initialization (the T-BC may still function if SDP inputs are pre-configured by firmware)
- If setting pin state fails, log an error but continue with remaining pin operations

### REQ-2: No impact on T-GM or other clock types

The DPLL input pin enablement must only occur when `clockType == "T-BC"`. T-GM and other clock type configurations must not be affected.

## Technical Design Notes

### Pin state model
Each DPLL pin has parent devices (typically EEC and PPS). The PPS parent is resolved dynamically at runtime by matching the pin's `ParentID` against cached DPLL devices, filtering by `ClockID` and device type (`"pps"`). This follows the same pattern as `DpllConfig.ActivePhaseOffsetPin` in `pkg/dpll/dpll.go`.

### Data structures

Target pin package labels for T-BC DPLL input pin setup:
```go
var bcDpllInputPins = []string{"REF0P", "REF0N"}
```

DPLL devices are cached in `E825PluginData.dpllDevices` alongside the existing `dpllPins` cache, populated via `getAllDpllDevices` during `populateDpllPins`.

### Integration point
The logic is called in `OnPTPConfigChangeE825`, within the `if tbcConfigured(nodeProfile)` block, after `bcDpllPinReset` application.

### DPLL netlink command structure
For each target pin, a `PinParentDeviceCtl` command is constructed:
- Pin ID: resolved from the fetched DPLL pin list by package label
- PPS parent: identified dynamically by matching `ParentID` to a device with `ClockID == pin.ClockID` and `GetDpllType(dev.Type) == "pps"`; state set to `PinStateSelectable` (value 3)
- EEC parent: unchanged (not included in command)

## Out of Scope

- Changing REF0P/REF0N priority values (they retain their default priorities from reset)
- Enabling REF0P/REF0N on the EEC parent device
- Disabling REF0P/REF0N on `tbc-ho-entry` (they should remain selectable; the DPLL auto-selects based on priority)
- Supporting platforms with different package labels for SDP input pins (only REF0P/REF0N are supported)
- Configuration of REF0P/REF0N via user-specified plugin options (this is automatic behavior)

## Testing Requirements

### Unit Tests
1. Verify that `OnPTPConfigChangeE825` with a T-BC profile calls the new pin enablement logic
2. Verify that REF0P and REF0N are set to `selectable` on PPS parent only
3. Verify that EEC parent state is not modified
4. Verify that T-GM profiles do not trigger DPLL input pin enablement
5. Verify graceful handling when REF0P/REF0N pins are not found in the DPLL pin dump

### Integration Verification
- On Dell platform: confirm pins `ETH01_SDP_TIMESYNC_2` (REF0P) and `ETH01_SDP_TIMESYNC_0` (REF0N) transition from `disconnected` to `selectable` on PPS parent when T-BC is configured
- On HPE platform: confirm pins `1PPS_IN1` (REF0P) and `1PPS_IN0` (REF0N) transition similarly
