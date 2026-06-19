#!/bin/sh
# 30-ready-notification — `ready-notification = pipevar:VARNAME` makes
# slinit pass a writable pipe fd to the child as $VARNAME, and hold the
# service in STARTING until the child writes "READY=1\n" to that fd.
#
# Probe: command sleeps 2s before signalling ready. While sleeping, state
# must be STARTING; after the write, state must be STARTED.
#
# (slinit's `ready-notification` is the pipefd/pipevar form, *not* the
# sd_notify NOTIFY_SOCKET protocol — that one only exists when
# `file-descriptor-store-max > 0` (process.go:942), so a plain
# ready-notification probe lives entirely in this pipevar shape.)

SVC="acceptance-test-ready"

trap 'svc_remove "$SVC"' EXIT INT TERM

# Two layers of escape for $NOTIFY_FD:
#  1. \$\$ → $$ in the heredoc (host shell would otherwise eat $$ as its
#     own PID).
#  2. slinit's parser pre-expands $VAR at config-load time
#     (parser.go:1000 expandEnvVarsForCommand). The doubled $$ collapses
#     to a single literal $ in the service description; only then does
#     the child shell see $NOTIFY_FD and expand it from the runtime env.
svc_deploy "$SVC" <<EOF
type = process
ready-notification = pipevar:NOTIFY_FD
command = /bin/sh -c 'sleep 2; printf "READY=1\n" > /dev/fd/\$\$NOTIFY_FD; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1

# Poll for STARTING within the first second — must NOT yet be STARTED.
sleep 1
_st_mid=$(svc_state "$SVC")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st_mid" in
    STARTING)
        echo "OK: $SVC held in STARTING until READY=1 (mid-state: $_st_mid)"
        ;;
    STARTED)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $SVC reached STARTED before READY=1 was sent"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $SVC unexpected mid-state '$_st_mid'"
        ;;
esac

# By 4s the READY=1 should have flowed and slinit should have transitioned.
wait_for_service "$SVC" "STARTED" 6 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED after READY=1"

test_summary
