#!/bin/sh
# Test: numa-mempolicy = bind on node 0. Requires CONFIG_NUMA and
# libnuma-aware slinit-runner. Alpine's virt kernel may be UP-only /
# without CONFIG_NUMA; skip when /sys/devices/system/node isn't there.

SVC="test-numa"

if [ ! -d /sys/devices/system/node ]; then
    echo "SKIP: kernel does not expose /sys/devices/system/node (CONFIG_NUMA off)"
    test_summary
    return 0
fi

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
numa-mempolicy = bind
numa-nodes = 0
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -r "/proc/$_pid/numa_maps" ]; then
    _line=$(head -1 "/proc/$_pid/numa_maps" 2>/dev/null)
    echo "OK: numa_maps accessible (first line: '${_line:-<empty>}')"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: numa_maps unreadable"
fi

test_summary
