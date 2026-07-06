#!/bin/sh
# 105-stop-command — `stop-command` runs before the signal is sent,
# with a chance to do graceful cleanup.
#
# Case 26 covers runtime-max-sec's interaction with stop; this one
# is the direct assertion that a stop-command's side-effect lands
# on disk before the service reaches STOPPED.

SVC="${ACCEPTANCE_NS_PREFIX}stopcmd"
MARKER="/tmp/acceptance-stopcmd-marker"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARKER"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while true; do sleep 60; done'
stop-command = /bin/sh -c 'echo stop-fired > $MARKER'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

# Before stop: marker must not exist.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARKER" ]; then
    echo "OK: stop-command not fired while STARTED"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker present too early"
fi

slinitctl --system stop "$SVC" 2>/dev/null
sleep 1

assert_eq "$(cat "$MARKER" 2>/dev/null)" "stop-fired" \
    "stop-command executed with expected side-effect"

assert_eq "$(svc_state "$SVC")" "STOPPED" "service reached STOPPED"

test_summary
