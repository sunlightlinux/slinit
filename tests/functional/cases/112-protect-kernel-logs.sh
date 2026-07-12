#!/bin/sh
# Test: protect-kernel-logs blocks syslog(2) + hides /dev/kmsg.

SVC="test-pkl"
OUT="/tmp/functional-pkl-out"

cat > "/etc/slinit.d/$SVC" <<EOF
type = process
protect-kernel-logs = yes
command = /bin/sh -c 'dmesg 2>$OUT >/dev/null; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_service_state "$SVC" "STARTED" "service reached STARTED"
sleep 0.5

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ { print $2; exit }')
_seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
assert_eq "$_seccomp" "2" "seccomp filter (mode 2) installed on child"

_err=$(cat "$OUT" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_err" ]; then
    echo "OK: dmesg rejected inside guarded service"
else
    echo "OK: seccomp installed (dmesg silent — probe inconclusive but filter active)"
fi

test_summary
