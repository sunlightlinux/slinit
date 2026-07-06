#!/bin/sh
# 135-normal-exit — scripted service exits 42 with normal-exit = 42.
# Expected: STOPPED, not FAILED.

SVC="${ACCEPTANCE_NS_PREFIX}nex"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = scripted
command = /bin/sh -c 'exit 42'
normal-exit = 42
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
# scripted starts then completes → STOPPED (not FAILED)
sleep 2
_state=$(svc_state "$SVC")

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_state" in
    STOPPED)
        echo "OK: exit 42 treated as normal → STOPPED"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: expected STOPPED, got '$_state'"
        ;;
esac

test_summary
