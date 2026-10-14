# PTP Parser Package

This package provides functionality to parse and process PTP (Precision Time Protocol) logs from various PTP daemons like `ptp4l` and `phc2sys`. It extracts metrics and events from log lines and maintains state information about the PTP configuration.

## Overview

The parser package consists of several components:

- **Metrics Extractors**: Parse log lines to extract PTP metrics
- **Event Parsers**: Parse log lines to extract PTP events
- **Constants**: Common constants used across the package

## Usage

### Basic Usage

```go
import (
    "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
)

// Create a new PTP4L extractor
ptp4lExtractor := parser.NewPTP4LExtractor()

// Create a new PHC2SYS extractor
phc2sysExtractor := parser.NewPhc2SysExtractor()

// Parse a log line
logLine := "ptp4l[365195.391]: [ptp4l.0.config] master offset -1 s2 freq -3972 path delay 89"
metrics, event, err := ptp4lExtractor.Extract(logLine)
if err != nil {
    // Handle error
}
```

### Parsing Different Types of Logs

#### PTP4L Logs

```go
// Parse summary metrics
summaryLog := "ptp4l[74737.942]: [ptp4l.0.config] rms 53 max 74 freq -16642 +/- 40 delay 1089 +/- 20"
metrics, event, err := ptp4lExtractor.Extract(summaryLog)

// Parse regular metrics
regularLog := "ptp4l[365195.391]: [ptp4l.0.config] master offset -1 s2 freq -3972 path delay 89"
metrics, event, err := ptp4lExtractor.Extract(regularLog)

// Parse events
eventLog := "ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER"
metrics, event, err := ptp4lExtractor.Extract(eventLog)

// Extract only events
event, err := ptp4lExtractor.ExtractEvent(eventLog)
```

#### PHC2SYS Logs

```go
// Parse summary metrics
summaryLog := "phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms 4 max 4 freq -76829 +/- 0 delay 1085 +/- 0"
metrics, event, err := phc2sysExtractor.Extract(summaryLog)

// Parse regular metrics
regularLog := "phc2sys[10522413.392]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset 8 s2 freq -6990 delay 502"
metrics, event, err := phc2sysExtractor.Extract(regularLog)
```

#### TS2PHC Poll Monitoring

The parser returns a single metric and/or event per log line. ts2phc
`adding tstamp` and `poll returns zero, no events` lines are returned as typed
`Ts2PhcEvent` payloads on `PTPEvent`. Poll-cycle tracking lives in
`pkg/daemon/ts2phc.go`, where each process can resolve PHC devices to its
configured interfaces. Offset lines return both a metric and a successful-poll
`Ts2PhcEvent`; the event consumes the corresponding timestamp independently of
metric processing. Timeouts and cycle rollover report FREERUN for remaining
managed clocks, without emitting metrics for unconfigured PHCs. Poll state is
isolated per process and reset when ts2phc starts.

`selecting /dev/ptpN for synchronization` and `unselecting /dev/ptpN for
synchronization` lines also return event-only `Ts2PhcEvent` payloads. `PHCDevice`
identifies the clock and the `Role` field (`TS2PHCRole`) describes its new role.
An empty role means this is not a role-change event. For compatibility across
versions and modes, these messages are normalized as:

| Log message | `Role` |
| --- | --- |
| `selecting /dev/ptpN for synchronization` | `sink` |
| `unselecting /dev/ptpN for synchronization` | `unmanaged` |
| `selecting /dev/ptpN as the reference clock` | `source` |
| `no reference clock, selecting /dev/ptpN by default` | `source` |

Both reference-selection messages clear ts2phc's `is_target` flag, so the PHC
must not be monitored as a synchronization target. Messages without a PHC, such
as `assuming external reference clock` and `reference clock not ready, waiting...`,
do not generate selection events.

The daemon populates a per-process role map from the configured PHC devices,
initializing all of them as sinks at configuration load and restart. Multiple
interface aliases sharing a PHC share its role. Role changes only update known
PHCs; unknown devices are not implicitly enrolled by log messages.

Only sinks generate synthetic failure metrics for pending or missing timestamps.
Source and unmanaged timestamps remain stored, but are marked handled to avoid
failure metrics. Source timestamps are found directly by role in the current
cycle, without a separate source cache. The source-minus-sink offset furthest
from zero is preferred with its sign preserved; without another source device's
timestamp, the largest absolute peer difference is used. All offsets are converted from seconds to nanoseconds.
Role changes also update source status on pending timestamps, clearing stale
references on demotion. Reselection as a sink enables future samples without
reopening already-handled timestamps. Unselection alone does not imply source
status.

### Metrics Structure

The parser returns metrics in the following structure:

```go
type Metrics struct {
    ConfigName string  // Configuration name (e.g., "ptp4l.0.config")
    From       string  // Source process
    Iface      string  // Interface name or CLOCK_REALTIME
    Offset     float64 // Time offset
    MaxOffset  float64 // Maximum time offset
    FreqAdj    float64 // Frequency adjustment
    Delay      float64 // Path delay
    ClockState string  // Clock state (LOCKED, FREERUN, etc.)
    Source     string  // Source of the metrics (master, phc, sys, etc.)
}
```

### Event Structure

For PTP4L events, the parser returns events in the following structure:

```go
type PTPEvent struct {
    PortID int    // Port ID
    Iface  string // Interface name
    Role   PTPPortRole // Port role (SLAVE, MASTER, PASSIVE, etc.)
    Raw    string // Raw event message
}
```

### PTP Port Roles

The parser supports the following PTP port roles:

```go
const (
    UNKNOWN   PTPPortRole = "UNKNOWN"
    MASTER    PTPPortRole = "MASTER"
    SLAVE     PTPPortRole = "SLAVE"
    PASSIVE   PTPPortRole = "PASSIVE"
    FAULTY    PTPPortRole = "FAULTY"
    LISTENING PTPPortRole = "LISTENING"
)
```

## Error Handling

The parser returns errors in the following cases:

- Invalid log line format
- Missing required fields
- Invalid numeric values
- Invalid state transitions
- Invalid port numbers

Example error handling:

```go
metrics, event, err := extractor.Extract(logLine)
if err != nil {
    // Handle error
    log.Printf("Error parsing log line: %v", err)
    return
}

// Check if we got metrics or an event
if metrics != nil {
    // Process metrics
    fmt.Printf("Offset: %f, FreqAdj: %f\n", metrics.Offset, metrics.FreqAdj)
}

if event != nil {
    // Process event
    fmt.Printf("Port %d: %s\n", event.PortID, event.Role)
}
```

## Testing

The package includes comprehensive tests for all functionality. You can run the tests using:

```bash
go test -v ./pkg/parser
```

## Log Line Formats

### PTP4L Log Formats

1. Summary Metrics:

    ```plain
    ptp4l[74737.942]: [ptp4l.0.config] rms 53 max 74 freq -16642 +/- 40 delay 1089 +/- 20
    ```

2. Regular Metrics:

    ```plain
    ptp4l[365195.391]: [ptp4l.0.config] master offset -1 s2 freq -3972 path delay 89
    ```

3. Events:

    ```plain
    ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER
    ```

### PHC2SYS Log Formats

1. Summary Metrics:

    ```plain
    phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms 4 max 4 freq -76829 +/- 0 delay 1085 +/- 0
    ```

2. Regular Metrics:

    ```plain
    phc2sys[10522413.392]: [ptp4l.0.config:6] CLOCK_REALTIME phc offset 8 s2 freq -6990 delay 502
    ```

## Available Extractors

The package provides extractors for the following PTP daemons:

- **PTP4L**: `NewPTP4LExtractor()` - Extracts metrics and events from ptp4l logs
- **PHC2SYS**: `NewPhc2SysExtractor()` - Extracts metrics from phc2sys logs
- **TS2PHC**: `NewTS2PHCExtractor()` - Extracts metrics from ts2phc logs
- **SYNCE**: `NewSyncEExtractor()` - Extracts metrics from synce logs
- **GNSS**: `NewGNSSExtractor()` - Extracts metrics from GNSS logs
- **GM**: `NewGMExtractor()` - Extracts metrics from grandmaster logs
- **DPLL**: `NewDPLLExtractor()` - Extracts metrics from DPLL logs

## Contributing

When adding new features or modifying existing ones:

1. Add appropriate tests
2. Update documentation
3. Follow the existing code style
4. Handle all error cases
5. Maintain backward compatibility
