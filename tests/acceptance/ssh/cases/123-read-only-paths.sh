#!/bin/sh
# 123-read-only-paths — bind-mounts the named paths read-only inside
# the service's mount namespace.

SVC="${ACCEPTANCE_NS_PREFIX}ropath"
WORK="/tmp/acceptance-ropath"

cleanup() {
    svc_remove "$SVC"
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WORK"
echo original >"$WORK/file"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
read-only-paths = $WORK
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
sleep 0.3

# Mountinfo has an ro entry for $WORK inside the namespace.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE " $WORK [^-]*\bro\b" "/proc/$_pid/mountinfo" 2>/dev/null; then
    echo "OK: $WORK bind-mounted read-only in namespace"
else
    _entry=$(grep " $WORK " /proc/$_pid/mountinfo 2>/dev/null | head -1)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no ro flag on $WORK — mount entry: $_entry"
fi

# Host copy remains writable.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo probe > "$WORK/host-write" 2>/dev/null; then
    rm -f "$WORK/host-write"
    echo "OK: host $WORK still writable"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: host $WORK became read-only"
fi

test_summary
