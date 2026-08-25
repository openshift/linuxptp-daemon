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

# poll_current_state waits until CurrentState for RESOURCE contains the given string.
poll_current_state() {
    local want="$1"
    local attempts=20
    for ((i=0; i<attempts; i++)); do
        CURRENT="$("$TMPDIR/consumer" \
            --api-url "http://localhost:$API_PORT" \
            --resource "$RESOURCE" \
            --current-state 2>/dev/null)" || true
        if echo "$CURRENT" | grep -q "$want"; then
            return 0
        fi
        sleep 0.5
    done
    return 1
}

# ── Preflight ──
check_port "$API_PORT"
check_port "$CONSUMER_PORT"

# ── Build ──
echo "Building binaries..."
(cd "$ROOT_DIR" && make)
cp "$ROOT_DIR/bin/cloud-event-proxy" "$TMPDIR/cloud-event-proxy"
(cd "$ROOT_DIR" && go build --mod=vendor -o "$TMPDIR/consumer" ./test/consumer/)
(cd "$ROOT_DIR" && go build --mod=vendor -o "$TMPDIR/ipc-sender" ./test/ipc-sender/)

# ── Start ipc-sender ──
# Message 0 (immediate): clock_class=6, pre-populates cache → snapshot
# Message 1 (SIGUSR1):   clock_class=7, live event
# Message 2 (SIGUSR1):   gnss_state, live event (wrong resource for subscriber)
MESSAGES='[
  {"message":{"version":1,"type":"clock_class","profile":"ptp4l.0.config","values":{"clock_class":6}}},
  {"message":{"version":1,"type":"clock_class","profile":"ptp4l.0.config","values":{"clock_class":7}}},
  {"message":{"version":1,"type":"gnss_state","profile":"ts2phc.0.config","iface":"ens2f0","values":{"state":"SYNCHRONIZED"}}}
]'

echo "Starting ipc-sender..."
"$TMPDIR/ipc-sender" --socket "$SOCKET" --messages "$MESSAGES" >"$TMPDIR/sender.log" 2>&1 &
SENDER_PID=$!
PIDS+=($SENDER_PID)

# ── Start cloud-event-proxy ──
NODE_NAME="$NODE_NAME" "$TMPDIR/cloud-event-proxy" \
    --socket "$SOCKET" \
    --api-port "$API_PORT" \
    --store-path "$TMPDIR" >"$TMPDIR/proxy.log" 2>&1 &
PIDS+=("$!")

# ── Step 1: Verify snapshot — poll until clock_class appears ──
echo "Waiting for snapshot..."
if poll_current_state '"value":"6"'; then
    pass "CurrentState returned clock_class=6 from snapshot"
else
    echo "Proxy log:"; cat "$TMPDIR/proxy.log"
    echo "Sender log:"; cat "$TMPDIR/sender.log"
    fail "CurrentState did not return expected event"
fi

# ── Step 2: Start consumer in subscribe mode ──
CONSUMER_LOG="$TMPDIR/consumer.log"
"$TMPDIR/consumer" \
    --port "$CONSUMER_PORT" \
    --api-url "http://localhost:$API_PORT" \
    --resource "$RESOURCE" \
    > "$CONSUMER_LOG" 2>"$TMPDIR/consumer.err" &
PIDS+=("$!")
sleep 1

# ── Step 3: Signal ipc-sender to send clock_class=7 (live event) ──
echo "Signaling clock_class=7..."
kill -USR1 "$SENDER_PID"

# Poll subscriber log for the live event
ATTEMPTS=20
RECEIVED=false
for ((i=0; i<ATTEMPTS; i++)); do
    if grep -q '"value":"7"' "$CONSUMER_LOG"; then
        RECEIVED=true
        break
    fi
    sleep 0.5
done

if $RECEIVED; then
    pass "Subscriber received live clock_class=7 push event"
else
    echo "Consumer log:"; cat "$CONSUMER_LOG"
    echo "Consumer err:"; cat "$TMPDIR/consumer.err"
    echo "Sender log:"; cat "$TMPDIR/sender.log"
    fail "Subscriber did not receive clock class event"
fi

# ── Step 4: Signal ipc-sender to send gnss_state — subscriber should NOT receive it ──
LINES_BEFORE=$(wc -l < "$CONSUMER_LOG")
echo "Signaling gnss_state..."
kill -USR1 "$SENDER_PID"

# Give it time to propagate (if it were going to)
sleep 2

LINES_AFTER=$(wc -l < "$CONSUMER_LOG")
if [ "$LINES_AFTER" -eq "$LINES_BEFORE" ]; then
    pass "Subscriber correctly did NOT receive GNSS event"
else
    fail "Subscriber received unexpected GNSS event. New lines: $(tail -n +$((LINES_BEFORE+1)) "$CONSUMER_LOG")"
fi

echo ""
echo "All tests passed."
exit 0
