#!/bin/sh
# 62-soft-reboot-snapshot — restore an operator-intent snapshot at boot.
#
# Soft-reboot rolls a fresh slinit binary onto a long-running box without
# rebooting the kernel. The outgoing daemon writes its intent (activated
# services, pins, triggers, global env) to /run/slinit/soft-reboot-snapshot.json
# in OnPreShutdown; the new daemon, started by syscall.Exec, picks it up
# via --restore-from-snapshot=<path> and re-applies the intent before the
# control socket opens.
#
# Testing the full softreboot loop in-place would re-exec the live PID 1
# and racey-up the suite. Instead we exercise the *restore* half end-to-end
# in a nested container slinit:
#   - hand-craft a snapshot.json (the file the outgoing daemon would have
#     written, schema version 1)
#   - boot a nested slinit pointed at it
#   - assert the global env + service activation actually took effect
#
# Capture (the write half) has dedicated unit coverage in pkg/snapshot.

SUBDIR="/tmp/acceptance-test-sbsnap"
SVCDIR="$SUBDIR/svc.d"
SOCKET="$SUBDIR/slinit.sock"
LOG="$SUBDIR/slinit.log"
SNAP="$SUBDIR/snapshot.json"
ACTIVATED_SVC="acceptance-test-sbsnap-activated"
PIN_SVC="acceptance-test-sbsnap-pinned"
MARKER="$SUBDIR/activated.mark"
SLINIT_PID=""

cleanup() {
    if [ -n "$SLINIT_PID" ]; then
        kill -TERM "$SLINIT_PID" 2>/dev/null
        for _ in 1 2 3; do
            kill -0 "$SLINIT_PID" 2>/dev/null || break
            sleep 1
        done
        kill -KILL "$SLINIT_PID" 2>/dev/null
    fi
    rm -rf "$SUBDIR"
}
trap cleanup EXIT INT TERM

rm -rf "$SUBDIR"
mkdir -p "$SVCDIR"

# Boot stub — internal, no dependencies. The snapshot is what brings the
# other services up, NOT a depends-on edge, so we can prove the restore
# is the cause of activation.
cat > "$SVCDIR/boot" <<EOF
type = internal
EOF

# Activated-by-snapshot service: load-only by default (no auto-start
# edge). The snapshot's "activated": true should pull it to STARTED.
cat > "$SVCDIR/$ACTIVATED_SVC" <<EOF
type = process
command = /bin/sh -c 'touch $MARKER; while :; do sleep 60; done'
restart = false
EOF

# Pinned-stop-by-snapshot service: same shape, but the snapshot pins it
# stopped. We then prove a start request is refused — the pin survived.
cat > "$SVCDIR/$PIN_SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

# Hand-roll the snapshot the outgoing daemon would have written. JSON
# schema lives at pkg/snapshot/schema.go (Version=1).
cat > "$SNAP" <<EOF
{
  "version": 1,
  "written_at": "2026-06-28T00:00:00Z",
  "services": [
    { "name": "$ACTIVATED_SVC", "activated": true },
    { "name": "$PIN_SVC",       "pinned_stop": true }
  ],
  "global_env": [
    "ACCEPTANCE_SBSNAP_KEY=hello-from-snapshot"
  ]
}
EOF

# --run-mode=keep tells the daemon NOT to re-stage /run (we are already
# in a live system; restaging would hide the snapshot file the parent
# wrote there). --restore-from-snapshot=<path> drives applySnapshot
# before the control socket goes up so reads always see post-restore state.
nohup slinit -o -m -p "$SOCKET" -d "$SVCDIR" -t boot \
    --restore-from-snapshot="$SNAP" --run-mode=keep \
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
    echo "OK: nested slinit (pid $SLINIT_PID) opened $SOCKET"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: socket never appeared. tail of log:"
    tail -20 "$LOG" 2>/dev/null | sed 's/^/  | /'
    test_summary
    exit 1
fi

# --- Probe 1: global env replayed from snapshot ---------------------
_env=$(slinitctl --socket-path "$SOCKET" getallenv-global 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_env" in
    *"ACCEPTANCE_SBSNAP_KEY=hello-from-snapshot"*)
        echo "OK: global env restored (ACCEPTANCE_SBSNAP_KEY=hello-from-snapshot)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: ACCEPTANCE_SBSNAP_KEY missing from restored env"
        echo "$_env" | sed 's/^/  | /'
        ;;
esac

# --- Probe 2: activated service reached STARTED ----------------------
# Wait briefly: applySnapshot kicks Start() but the process still needs
# to fork and the state machine to land at STARTED.
_st=""
_e=0
while [ "$_e" -lt 8 ]; do
    _st=$(slinitctl --socket-path "$SOCKET" status "$ACTIVATED_SVC" 2>/dev/null \
        | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: $ACTIVATED_SVC pulled to STARTED via snapshot's activated:true"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $ACTIVATED_SVC stuck at '$_st' (snapshot activated:true ignored?)"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARKER" ]; then
    echo "OK: marker present — restored service actually executed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker $MARKER missing despite STARTED state"
fi

# --- Probe 3: pinned-stop survived restore --------------------------
# Pin info lives on the service record. We don't have a dedicated
# `slinitctl pin-status` so we infer the pin two ways: (a) the service
# stays STOPPED on its own, (b) an explicit start request is refused
# while the pin is in effect.
_pst=$(slinitctl --socket-path "$SOCKET" status "$PIN_SVC" 2>/dev/null \
    | awk '/State:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_pst" = "STOPPED" ]; then
    echo "OK: $PIN_SVC remains STOPPED post-restore (no auto-start)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $PIN_SVC unexpectedly in '$_pst' (pinned_stop ignored?)"
fi

# Refused-start probe: with pinned_stop, slinitctl start should non-zero
# and the state should stay STOPPED. We capture both signals.
_so=$(slinitctl --socket-path "$SOCKET" start "$PIN_SVC" 2>&1)
_src=$?
_pst2=$(slinitctl --socket-path "$SOCKET" status "$PIN_SVC" 2>/dev/null \
    | awk '/State:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_src" -ne 0 ] && [ "$_pst2" = "STOPPED" ]; then
    echo "OK: start refused while pinned-stop (rc=$_src, state still STOPPED)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pinned-stop did NOT block start (rc=$_src, state=$_pst2, out=$_so)"
fi

test_summary
