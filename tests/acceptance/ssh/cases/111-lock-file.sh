#!/bin/sh
# 111-lock-file — `lock-file` is grabbed with flock(2) before exec;
# a second instance holding the same lock refuses.

SVC="${ACCEPTANCE_NS_PREFIX}lock"
LOCK="/tmp/acceptance-lockfile.lock"

cleanup() {
    svc_remove "$SVC"
    rm -f "$LOCK"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while true; do sleep 60; done'
lock-file = $LOCK
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$LOCK" ]; then
    echo "OK: lock-file exists on disk at $LOCK"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no lock file at $LOCK"
    test_summary
    exit 0
fi

# Someone else trying to flock the same file must be blocked. Use
# `flock -n -x` (non-blocking exclusive) — returns immediately with
# non-zero when the lock is held.
_TESTS_RUN=$((_TESTS_RUN + 1))
if flock -n -x "$LOCK" true 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: flock -n succeeded — service does not hold the lock"
else
    echo "OK: external flock -n rejected — service holds the exclusive lock"
fi

# Stop → lock file removed (or at least unheld).
slinitctl --system stop "$SVC" 2>/dev/null
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if flock -n -x "$LOCK" true 2>/dev/null; then
    echo "OK: lock released after stop (external flock succeeds)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: lock still held after stop"
fi

test_summary
