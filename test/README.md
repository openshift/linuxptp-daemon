# Manual Testing

This directory contains three tools for testing the cloud-event-proxy end-to-end without a running PTP daemon.

## Build

```bash
go build -o bin/cloud-event-proxy ./cmd/cloud-event-proxy/
go build -o bin/consumer ./test/consumer/
go build -o bin/ipc-sender ./test/ipc-sender/
```

## Start the cloud-event-proxy

In terminal 1:

```bash
export NODE_NAME=worker-0
bin/cloud-event-proxy \
    --socket /tmp/events.sock \
    --api-port 9043 \
    --store-path /tmp
```

The proxy creates a Unix socket at `/tmp/events.sock` and listens for IPC messages from the daemon (or `ipc-sender`). The REST API is available at `http://localhost:9043`.

## Start the consumer

In terminal 2, subscribe to PTP lock state events:

```bash
bin/consumer \
    --port 9090 \
    --api-url http://localhost:9043 \
    --resource /cluster/node/worker-0/sync/ptp-status/lock-state
```

The consumer registers a subscription with the proxy and prints every matching CloudEvent it receives to stdout. It runs until you press Ctrl-C.

To subscribe to a different resource, change the `--resource` flag. See [Resource paths](#resource-paths) below.

## Send IPC events

In terminal 3, use `ipc-sender` to simulate daemon events. Each invocation connects to the proxy socket, sends one message, and exits.

### PTP state

```bash
bin/ipc-sender --socket /tmp/events.sock \
    --type ptp_state \
    --profile ptp4l.0.config \
    --iface ens2f0 \
    --state LOCKED
```

Valid states: `LOCKED`, `HOLDOVER`, `FREERUN`

### OS clock state

```bash
bin/ipc-sender --socket /tmp/events.sock \
    --type os_clock_state \
    --profile ptp4l.0.config \
    --state LOCKED
```

### Clock class

```bash
bin/ipc-sender --socket /tmp/events.sock \
    --type clock_class \
    --profile ptp4l.0.config \
    --clock-class 6
```

Common clock class values: `6` (locked to PRTC), `7` (holdover in-spec), `140` (holdover out-of-spec), `248` (free-running)

### GNSS state

```bash
bin/ipc-sender --socket /tmp/events.sock \
    --type gnss_state \
    --profile ts2phc.0.config \
    --iface ens2f0 \
    --state SYNCHRONIZED
```

Valid states: `SYNCHRONIZED`, `ACQUIRING_SYNC`, `ANTENNA_DISCONNECTED`, `BOOTING`, `ANTENNA_SHORT_CIRCUIT`, `FAILURE_MULTIPATH`, `FAILURE_NOFIX`, `FAILURE_LOW_SNR`, `FAILURE_PLL`

### SyncE state

```bash
bin/ipc-sender --socket /tmp/events.sock \
    --type synce_state \
    --profile synce4l.0.config \
    --iface ens7f0 \
    --state LOCKED
```

### Overall sync state

```bash
bin/ipc-sender --socket /tmp/events.sock \
    --type sync_state \
    --profile ptp4l.0.config \
    --state FREERUN
```

### Cache clear

```bash
bin/ipc-sender --socket /tmp/events.sock \
    --type cache_clear
```

This tells the proxy to drop all cached state. CurrentState queries will return 404 until new events arrive.

## Query CurrentState

Instead of subscribing, you can do a one-shot query for the current cached state of any resource:

```bash
bin/consumer \
    --api-url http://localhost:9043 \
    --resource /cluster/node/worker-0/sync/ptp-status/lock-state \
    --current-state
```

This prints the cached CloudEvent as JSON and exits. If no event has been cached for that resource, it exits with an error.

## Resource paths

Each IPC event type maps to an O-RAN resource path. The consumer subscribes by resource path, so it only receives events matching that path.

| IPC type | Resource path |
|----------|--------------|
| `ptp_state` | `/cluster/node/{node}/sync/ptp-status/lock-state` |
| `os_clock_state` | `/cluster/node/{node}/sync/sync-status/os-clock-sync-state` |
| `clock_class` | `/cluster/node/{node}/sync/ptp-status/clock-class` |
| `gnss_state` | `/cluster/node/{node}/sync/gnss-status/gnss-sync-status` |
| `synce_state` | `/cluster/node/{node}/sync/synce-status/lock-state` |
| `synce_clock_quality` | `/cluster/node/{node}/sync/synce-status/clock-quality` |
| `sync_state` | `/cluster/node/{node}/sync/sync-status/sync-state` |

Replace `{node}` with the value of `NODE_NAME` (or the hostname if unset).

## Automated test

Run `bash test/cloud-event-proxy/e2e.sh` to execute the full automated test suite. It builds all binaries, starts the proxy and consumer, sends events, and verifies correct delivery and filtering.
