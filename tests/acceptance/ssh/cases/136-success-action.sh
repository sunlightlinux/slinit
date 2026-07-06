#!/bin/sh
# 136-success-action — parse-only smoke: system-wide reboot/poweroff
# actions are unsafe on a live prod VM. Verify slinit accepts
# `success-action = none` cleanly and the scripted service reaches
# STOPPED after normal exit 0.

SVC="${ACCEPTANCE_NS_PREFIX}sxa"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = scripted
command = /bin/sh -c 'exit 0'
success-action = none
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
sleep 2
_state=$(svc_state "$SVC")

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_state" in
    STARTED|STOPPED)
        echo "OK: success-action=none parsed; state='$_state'"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: state='$_state'"
        ;;
esac

test_summary
