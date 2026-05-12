#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
TMPDIR="$(mktemp -d)"
SOCKET="$TMPDIR/events.sock"
NODE_NAME="test-node"
API_PORT=19043
CONSUMER_PORT=19090
RESOURCE="/cluster/node/$NODE_NAME/sync/ptp-status/clock-class"

PIDS=()

cleanup() {
    for pid in "${PIDS[@]}"; do
        kill "$pid" 2>/dev/null || true
    done
    sleep 0.5
    for pid in "${PIDS[@]}"; do
        kill -9 "$pid" 2>/dev/null || true
    done
    wait 2>/dev/null || true
    rm -rf "$TMPDIR"
}
trap cleanup EXIT

pass() { echo "PASS: $1"; }
fail() { echo "FAIL: $1"; exit 1; }

check_port() {
    if lsof -ti :"$1" >/dev/null 2>&1; then
        echo "ERROR: port $1 already in use"
        exit 1
    fi
}

# ── Preflight ──
check_port "$API_PORT"
check_port "$CONSUMER_PORT"

# ── Build ──
echo "Building binaries..."
(cd "$ROOT_DIR" && go build -o "$TMPDIR/cloud-event-proxy" ./cmd/cloud-event-proxy/)
(cd "$ROOT_DIR" && go build -o "$TMPDIR/consumer" ./test/consumer/)
(cd "$ROOT_DIR" && go build -o "$TMPDIR/ipc-sender" ./test/ipc-sender/)

# ── Start cloud-event-proxy ──
NODE_NAME="$NODE_NAME" "$TMPDIR/cloud-event-proxy" \
    --socket "$SOCKET" \
    --api-port "$API_PORT" \
    --store-path "$TMPDIR" >"$TMPDIR/proxy.log" 2>&1 &
PIDS+=($!)
sleep 1

# ── Step 1: Send initial clock class event (clock_class=6) ──
echo "Sending clock_class=6..."
"$TMPDIR/ipc-sender" --socket "$SOCKET" --type clock_class --profile ptp4l.0.config --clock-class 6
sleep 0.5

# ── Step 2: Start consumer in subscribe mode ──
CONSUMER_LOG="$TMPDIR/consumer.log"
"$TMPDIR/consumer" \
    --port "$CONSUMER_PORT" \
    --api-url "http://localhost:$API_PORT" \
    --resource "$RESOURCE" \
    > "$CONSUMER_LOG" 2>"$TMPDIR/consumer.err" &
PIDS+=($!)
sleep 1

# ── Step 3: Query CurrentState — should return the cached clock_class=6 event ──
echo "Querying CurrentState..."
CURRENT="$("$TMPDIR/consumer" \
    --api-url "http://localhost:$API_PORT" \
    --resource "$RESOURCE" \
    --current-state 2>/dev/null)" || true

if echo "$CURRENT" | grep -q "clock-class"; then
    pass "CurrentState returned cached clock class event"
else
    echo "CurrentState output: $CURRENT"
    echo "Proxy log:"; cat "$TMPDIR/proxy.log"
    fail "CurrentState did not return expected event"
fi

# ── Step 4: Send new clock class event (clock_class=7) — subscriber should receive it ──
echo "Sending clock_class=7..."
"$TMPDIR/ipc-sender" --socket "$SOCKET" --type clock_class --profile ptp4l.0.config --clock-class 7
sleep 1

if grep -q "clock-class" "$CONSUMER_LOG"; then
    pass "Subscriber received clock class push event"
else
    echo "Consumer log:"; cat "$CONSUMER_LOG"
    echo "Consumer err:"; cat "$TMPDIR/consumer.err"
    fail "Subscriber did not receive clock class event"
fi

# ── Step 5: Send GNSS event — subscriber should NOT receive it ──
LINES_BEFORE=$(wc -l < "$CONSUMER_LOG")
echo "Sending gnss_state=SYNCHRONIZED..."
"$TMPDIR/ipc-sender" --socket "$SOCKET" --type gnss_state --profile ts2phc.0.config --iface ens2f0 --state SYNCHRONIZED
sleep 1

LINES_AFTER=$(wc -l < "$CONSUMER_LOG")
if [ "$LINES_AFTER" -eq "$LINES_BEFORE" ]; then
    pass "Subscriber correctly did NOT receive GNSS event"
else
    fail "Subscriber received unexpected GNSS event. New lines: $(tail -n +$((LINES_BEFORE+1)) "$CONSUMER_LOG")"
fi

echo ""
echo "All tests passed."
exit 0
