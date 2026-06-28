#!/bin/sh
# 69-escalating-shutdown — repeated shutdown signals escalate.
#
# pkg/eventloop/loop.go:escalateShutdown counts repeated shutdown signals
# during an ongoing shutdown:
#
#   count == 2 → Notice "Received X again, reducing emergency timeout
#                to 25%", log services still blocking shutdown.
#   count >= 3 → Error "killing all processes and forcing exit", send
#                SIGKILL to every active service and trip forceExitCh.
#
# Replacing PID 1 is destructive, so the test drives a nested container
# slinit instead. A "stubborn" service traps SIGTERM and stays alive,
# guaranteeing slinit sits in the shutting-down state long enough for
# us to deliver the second SIGTERM and observe the escalation in its
# log output.

SUBDIR="/tmp/acceptance-test-escal"
SVCDIR="$SUBDIR/svc.d"
SOCKET="$SUBDIR/slinit.sock"
LOG="$SUBDIR/slinit.log"
SLINIT_PID=""

cleanup() {
    if [ -n "$SLINIT_PID" ]; then
        kill -KILL "$SLINIT_PID" 2>/dev/null
        wait "$SLINIT_PID" 2>/dev/null
    fi
    rm -rf "$SUBDIR"
}
trap cleanup EXIT INT TERM

rm -rf "$SUBDIR"
mkdir -p "$SVCDIR"

# Internal boot service so the daemon has something to "complete" before
# shutdown — the nested slinit insists on a target.
cat > "$SVCDIR/boot" <<EOF
type = internal
depends-on: stubborn
EOF

# Stubborn service: ignores SIGTERM via `trap`. Slinit's stop sequence
# will send SIGTERM (per stop-signal default), the trap absorbs it, and
# the daemon is stuck in shutting-down state with stubborn STOPPING.
# stop-timeout is bumped so the daemon doesn't auto-escalate to SIGKILL
# before we deliver the second SIGTERM ourselves.
cat > "$SVCDIR/stubborn" <<'EOF'
type = process
command = /bin/sh -c "trap '' TERM; while :; do sleep 60; done"
restart = false
stop-timeout = 60
EOF

# --console-level debug captures the Notice/Error lines we assert on.
nohup slinit -o -m -p "$SOCKET" -d "$SVCDIR" -t boot \
    --console-level debug \
    >"$LOG" 2>&1 &
SLINIT_PID=$!

_e=0
while [ "$_e" -lt 6 ]; do
    [ -S "$SOCKET" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -S "$SOCKET" ]; then
    echo "OK: nested slinit booted (pid $SLINIT_PID)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: socket never appeared"
    tail -20 "$LOG" 2>/dev/null | sed 's/^/  | /'
    test_summary
    exit 1
fi

# Make sure stubborn is in STARTED before we trigger the shutdown — a
# SIGTERM mid-start would short-circuit the path we want to exercise.
_e=0
while [ "$_e" -lt 6 ]; do
    _st=$(slinitctl --socket-path "$SOCKET" status stubborn 2>/dev/null \
        | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: stubborn STARTED — ready to test escalation"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stubborn stuck at '$_st'"
    test_summary
    exit 1
fi

# --- First SIGTERM: container mode handles it as graceful halt -------
kill -TERM "$SLINIT_PID"
# Give the event loop a beat to land in the shutting-down branch.
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "initiating graceful halt" "$LOG" 2>/dev/null; then
    echo "OK: 1st SIGTERM triggered the container halt path"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: container halt not logged. tail:"
    tail -15 "$LOG" 2>/dev/null | sed 's/^/  | /'
fi

# stubborn ignores TERM, so slinit is still alive in shutting-down.
_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 "$SLINIT_PID" 2>/dev/null; then
    echo "OK: slinit still up after 1st SIGTERM (stubborn blocking)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit exited prematurely (stubborn trap failed?)"
fi

# --- Second SIGTERM: escalation level 2 -----------------------------
# count==2 → "reducing emergency timeout" + blockingServices log.
kill -TERM "$SLINIT_PID"
sleep 2

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "reducing emergency timeout" "$LOG" 2>/dev/null; then
    echo "OK: 2nd SIGTERM logged emergency-timeout reduction"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 'reducing emergency timeout' missing. tail:"
    tail -20 "$LOG" 2>/dev/null | sed 's/^/  | /'
fi

# The blocker log mentions the service name at least once after the
# escalation kicks in.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -A20 "reducing emergency timeout" "$LOG" 2>/dev/null \
        | grep -q "stubborn"; then
    echo "OK: blocker reporter named the stubborn service"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: blocker report didn't mention stubborn. tail:"
    tail -20 "$LOG" 2>/dev/null | sed 's/^/  | /'
fi

# --- Third SIGTERM: escalation level 3 — KILL + forceExit -----------
kill -TERM "$SLINIT_PID"
# Wait for slinit to actually exit (force path). Up to 10s — the timer
# reset above gave it a 25% slice, but with SIGKILL the loop should
# return promptly.
_e=0
while [ "$_e" -lt 10 ]; do
    kill -0 "$SLINIT_PID" 2>/dev/null || break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if ! kill -0 "$SLINIT_PID" 2>/dev/null; then
    echo "OK: slinit exited after 3rd SIGTERM (force path)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit still alive after 3rd SIGTERM (force path didn't fire)"
    kill -KILL "$SLINIT_PID" 2>/dev/null
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE "killing all processes|forcing exit" "$LOG" 2>/dev/null; then
    echo "OK: log carries the SIGKILL/force-exit Error line"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: SIGKILL/force-exit not logged. tail:"
    tail -20 "$LOG" 2>/dev/null | sed 's/^/  | /'
fi

# Prevent cleanup() from trying to kill an already-reaped PID.
SLINIT_PID=""

test_summary
