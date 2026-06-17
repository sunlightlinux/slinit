#!/bin/sh
# 24-pin — `slinitctl --pin stop SVC` pins the service stopped; subsequent
# `start` requests must be refused until `unpin`. Symmetrically, `--pin start`
# pins started so `stop` is refused.

SVC="acceptance-test-pin"

trap 'slinitctl --system unpin "$SVC" 2>/dev/null || true; svc_remove "$SVC"' \
    EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

# Start the service first so we can pin-stop it.
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC initial STARTED"

# pin-stop: stop AND pin in stopped state. Subsequent start must be refused.
slinitctl --system --pin stop "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STOPPED" 10 || true
assert_service_state "$SVC" "STOPPED" "$SVC STOPPED after --pin stop"

# Attempted start: slinitctl returns non-zero ("pinned stopped").
_out=$(slinitctl --system start "$SVC" 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: start refused while pinned-stopped (rc=$_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: start succeeded while pinned-stopped: $_out"
fi
assert_service_state "$SVC" "STOPPED" "$SVC still STOPPED after refused start"

# unpin then start should work normally.
slinitctl --system unpin "$SVC" >/dev/null 2>&1
slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED after unpin"

# Symmetric case: pin-started must refuse stop. Important detail —
# `--pin start` on an *already-started* service is a no-op: the daemon
# short-circuits with RplyAlreadyStarted before reaching the pin-application
# code path (pkg/control/connection.go handleStartService). So stop first,
# then --pin start fresh to actually attach the pin.
slinitctl --system stop "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STOPPED" 10 || true
slinitctl --system --pin start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED after --pin start (from STOPPED)"

_out=$(slinitctl --system stop "$SVC" 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: stop refused while pinned-started (rc=$_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: stop succeeded while pinned-started: $_out"
fi
assert_service_state "$SVC" "STARTED" "$SVC still STARTED after refused stop"

test_summary
