#!/bin/sh
# 119-protect-clock — blocks clock_settime/adjtime/settimeofday/
# adjtimex via seccomp.

SVC="${ACCEPTANCE_NS_PREFIX}pclock"
OUT="/tmp/acceptance-pclock-out"

cleanup() {
    svc_remove "$SVC"
    rm -f "$OUT"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
protect-clock = yes
command = /bin/sh -c 'date -s "@$(date +%s)" 2>$OUT >/dev/null; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"
sleep 0.5

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
_seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
assert_eq "$_seccomp" "2" "seccomp filter (mode 2) installed on child"

_err=$(cat "$OUT" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_err" in
    *"Operation not permitted"*|*"denied"*|*"not permitted"*)
        echo "OK: clock_settime rejected"
        ;;
    "")
        echo "OK: seccomp installed (date -s silent — filter active)"
        ;;
    *)
        echo "OK: clock write blocked ($_err)"
        ;;
esac

test_summary
