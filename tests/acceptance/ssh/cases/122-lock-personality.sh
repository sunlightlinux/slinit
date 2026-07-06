#!/bin/sh
# 122-lock-personality — blocks personality(2) via seccomp. The
# syscall is hard to invoke from a shell, so we assert seccomp mode
# 2 is active on the child and the service comes up cleanly.

SVC="${ACCEPTANCE_NS_PREFIX}lockp"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
lock-personality = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
_seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
assert_eq "$_seccomp" "2" "seccomp filter (mode 2) installed"

# personality(2) is hard to hit from POSIX shell; the filter presence
# is the observable signal. Also confirm the runner didn't abort.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -d "/proc/$_pid" ]; then
    echo "OK: service pid $_pid still running under lock-personality"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $_pid vanished"
fi

test_summary
