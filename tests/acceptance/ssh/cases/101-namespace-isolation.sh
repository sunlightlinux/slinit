#!/bin/sh
# 101-namespace-isolation — CLONE_NEWPID + CLONE_NEWNS + CLONE_NEWUTS
# isolation via namespace-pid / namespace-mount / namespace-uts.

SVC="${ACCEPTANCE_NS_PREFIX}namespace"
MARKER="/tmp/acceptance-namespace-out"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARKER"
}
trap cleanup EXIT INT TERM
cleanup

# The child prints its own /proc/self/status NSpid line — inside a
# fresh PID namespace, NSpid shows the host pid PLUS the namespace
# pid (usually 1). We assert on that "1" tail.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'cat /proc/self/status | grep -E "^NSpid|^Uts_ns" > $MARKER; echo hostname_inside=\$(hostname) >> $MARKER; while true; do sleep 60; done'
namespace-pid = yes
namespace-mount = yes
namespace-uts = yes
stop-timeout = 3
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "namespace service reached STARTED"

# Give the child a moment to write the marker.
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$MARKER" ]; then
    echo "OK: marker file written"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no marker file — service may not have executed"
    test_summary
    exit 0
fi

# NSpid line shape: "NSpid:\t<host_pid>\t<ns_pid>". Presence of a
# SECOND field is what proves PID namespace was unshared; the ns_pid
# value is small (1-3 usually) because our child sh(1) is very close
# to the top of its private PID tree. host and ns pids must differ
# (unshared namespace).
_TESTS_RUN=$((_TESTS_RUN + 1))
_line=$(grep '^NSpid:' "$MARKER")
_host_ns=$(echo "$_line" | awk '{print $2}')
_child_ns=$(echo "$_line" | awk '{print $NF}')
_field_count=$(echo "$_line" | awk '{print NF}')
if [ "$_field_count" -ge 3 ] && [ "$_host_ns" != "$_child_ns" ]; then
    echo "OK: PID namespace unshared (host=$_host_ns ns=$_child_ns)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: NSpid line has $_field_count fields — host=$_host_ns ns=$_child_ns"
    echo "      full line: $_line"
fi

# Confirm the ns pid is a small number (< 10) — proves the child is
# near the top of its private PID tree, not way down like it would
# be if run in the shared host namespace.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_child_ns" ] && [ "$_child_ns" -lt 10 ] 2>/dev/null; then
    echo "OK: child ns_pid ($_child_ns) is small — new namespace confirmed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: ns_pid = $_child_ns (expected < 10)"
fi

test_summary
