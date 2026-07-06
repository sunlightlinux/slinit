#!/bin/sh
# 126-inaccessible-paths — hides a path from the service by
# overmounting it with an empty inaccessible node.

SVC="${ACCEPTANCE_NS_PREFIX}inacc"
WORK="/tmp/acceptance-inacc"

cleanup() {
    svc_remove "$SVC"
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WORK"
echo secret >"$WORK/file"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
inaccessible-paths = $WORK
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
sleep 0.3

# There's a mount entry hiding $WORK inside the namespace.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q " $WORK " "/proc/$_pid/mountinfo" 2>/dev/null; then
    echo "OK: $WORK overmounted in namespace"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no mount entry for $WORK inside namespace"
fi

# Host still sees the original contents.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$WORK/file" ] && [ "$(cat $WORK/file)" = "secret" ]; then
    echo "OK: host $WORK still readable"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: host $WORK changed on the host side"
fi

test_summary
