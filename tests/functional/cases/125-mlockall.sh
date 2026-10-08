#!/bin/sh
# Test: mlockall = current+future locks the service's memory (VmLck > 0,
# via the libslinit-mlock.so preload) and raises RLIMIT_MEMLOCK to
# unlimited so the service may lock more itself.

SVC="test-mlk"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
mlockall = current+future
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=""
_i=0
while [ "$_i" -lt 5 ]; do
    _pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
    [ -n "$_pid" ] && [ "$_pid" != "0" ] && break
    sleep 0.2
    _i=$((_i + 1))
done

_line=$(awk '/^Max locked memory/' "/proc/$_pid/limits" 2>/dev/null)
_soft=$(printf '%s' "$_line" | awk '{ print $(NF-2) }')
_hard=$(printf '%s' "$_line" | awk '{ print $(NF-1) }')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_soft" = "unlimited" ] && [ "$_hard" = "unlimited" ]; then
    echo "OK: RLIMIT_MEMLOCK soft=unlimited hard=unlimited"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: RLIMIT_MEMLOCK not raised — line='$_line' (soft='$_soft' hard='$_hard')"
fi

# The lock itself: slinit-runner preloads libslinit-mlock.so, which calls
# mlockall(2) inside the service's main process, so VmLck is non-zero.
_lck=$(awk '/^VmLck:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_lck" ] && [ "$_lck" -gt 0 ]; then
    echo "OK: service memory locked (VmLck=${_lck} kB)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: service memory not locked (VmLck='${_lck}')"
fi

test_summary
