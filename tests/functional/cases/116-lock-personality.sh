#!/bin/sh
# Test: lock-personality blocks personality(2) via seccomp. The
# syscall is hard to invoke from shell, so we assert seccomp mode 2
# is active and the service came up cleanly.

SVC="test-lockp"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
lock-personality = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
_seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
assert_eq "$_seccomp" "2" "seccomp filter (mode 2) installed"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d "/proc/$_pid" ]; then
    echo "OK: service pid $_pid still running under lock-personality"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $_pid vanished"
fi

test_summary
