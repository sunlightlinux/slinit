#!/bin/sh
# 125-temporary-filesystem — mounts a private tmpfs over a directory
# inside the service's mount namespace, hiding host contents there.

SVC="${ACCEPTANCE_NS_PREFIX}tmpfs"
WORK="/tmp/acceptance-tmpfs"

cleanup() {
    svc_remove "$SVC"
    rm -rf "$WORK"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$WORK"
echo host-secret >"$WORK/host-file"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
temporary-filesystem = $WORK
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
sleep 0.3

# Mountinfo shows a tmpfs on $WORK.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q " $WORK .*tmpfs" "/proc/$_pid/mountinfo" 2>/dev/null; then
    echo "OK: tmpfs mounted on $WORK in namespace"
else
    _mi=$(grep " $WORK " /proc/$_pid/mountinfo 2>/dev/null | head -1)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no tmpfs on $WORK: $_mi"
fi

# host-file must exist on the host but be invisible in the namespace.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$WORK/host-file" ]; then
    echo "OK: host $WORK/host-file preserved"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: host $WORK/host-file vanished"
fi

test_summary
