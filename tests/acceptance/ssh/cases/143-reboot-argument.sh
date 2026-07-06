#!/bin/sh
# 143-reboot-argument — parse-only smoke test. Actually triggering a
# reboot on a live prod VM is out of scope; verifying the config
# loads and the service starts is enough.

SVC="${ACCEPTANCE_NS_PREFIX}rebarg"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = scripted
command = /bin/sh -c 'exit 0'
success-action = none
reboot-argument = recovery
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
sleep 2
_state=$(svc_state "$SVC")

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_state" in
    STARTED|STOPPED)
        echo "OK: reboot-argument parsed cleanly; state='$_state'"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: state='$_state'"
        ;;
esac

test_summary
