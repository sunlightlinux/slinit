#!/bin/sh
# Test: securebits = keep-caps,no-setuid-fixup parses and the child
# comes up. Kernel bits aren't easily inspectable from POSIX sh, so
# the observable signal is "parser accepted, child alive".

SVC="test-sbits"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
securebits = keep-caps,no-setuid-fixup
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d "/proc/$_pid" ]; then
    echo "OK: securebits config accepted; child pid=$_pid alive"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: child vanished"
fi

test_summary
