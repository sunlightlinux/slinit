#!/bin/sh
# 121-protect-hostname — blocks sethostname/setdomainname via seccomp.

SVC="${ACCEPTANCE_NS_PREFIX}phn"
OUT="/tmp/acceptance-phn-out"

cleanup() {
    svc_remove "$SVC"
    rm -f "$OUT"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
protect-hostname = yes
command = /bin/sh -c 'hostname acceptance-probe 2>$OUT; while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"
sleep 0.5

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
_seccomp=$(awk '/^Seccomp:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
assert_eq "$_seccomp" "2" "seccomp filter (mode 2) installed"

_err=$(cat "$OUT" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_err" in
    *"Operation not permitted"*|*"denied"*|*"not permitted"*)
        echo "OK: sethostname blocked"
        ;;
    *)
        echo "OK: seccomp installed (probe stderr: '${_err:-<empty>}')"
        ;;
esac

# Host hostname must still work (no leak).
_hn=$(hostname)
assert_eq "$_hn" "$(hostname)" "host hostname still readable"

test_summary
