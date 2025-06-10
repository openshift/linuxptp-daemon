# PTP Parser Package

This package provides functionality to parse and process PTP (Precision Time Protocol) logs from various PTP daemons like `ptp4l` and `phc2sys`. It extracts metrics and events from log lines and maintains state information about the PTP configuration.

## Overview

The parser package consists of several components:

- **Metrics Extractors**: Parse log lines to extract PTP metrics
- **Event Parsers**: Parse log lines to extract PTP events
- **State Management**: Maintain state information about PTP configuration
- **Constants**: Common constants used across the package

## Usage

### Basic Usage

```go
import (
    "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser"
    sstate "github.com/k8snetworkplumbingwg/linuxptp-daemon/pkg/parser/state"
)

// Create a new shared state
state := sstate.NewSharedState()

// Create a new PTP4L extractor
ptp4lExtractor := parser.NewPTP4LExtractor(state)

// Create a new PHC2SYS extractor
phc2sysExtractor := parser.NewPhc2SysExtractor(state)

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
```
ptp4l[74737.942]: [ptp4l.0.config] rms 53 max 74 freq -16642 +/- 40 delay 1089 +/- 20
```

2. Regular Metrics:
```
ptp4l[365195.391]: [ptp4l.0.config] master offset -1 s2 freq -3972 path delay 89
```

3. Events:
```
ptp4l[4268779.809]: [ptp4l.0.config] port 1: UNCALIBRATED to SLAVE on MASTER
```

### PHC2SYS Log Formats

1. Summary Metrics:
```
phc2sys[3560354.300]: [ptp4l.0.config] CLOCK_REALTIME rms 4 max 4 freq -76829 +/- 0 delay 1085 +/- 0
```

2. Regular Metrics:
```
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