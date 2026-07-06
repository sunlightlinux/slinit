#!/bin/sh
# 116-ready-check — `ready-check-command` polls until the service
# reports ready. Case 30 covers pipefd:3 notify; this one is the
# poll variant.

SVC="${ACCEPTANCE_NS_PREFIX}readycheck"
READY="/tmp/acceptance-readycheck-marker"

cleanup() {
    svc_remove "$SVC"
    rm -f "$READY"
}
trap cleanup EXIT INT TERM
cleanup

# Service takes 2s to become "ready" (touches the marker after
# sleeping), then stays up. ready-check-command must succeed only
# once the marker exists.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c '(sleep 2; touch $READY) & while true; do sleep 60; done'
ready-check-command = /bin/sh -c 'test -e $READY'
ready-check-interval = 500ms
start-timeout = 15
restart = no
EOF

_start=$(date +%s)
slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 15
_elapsed=$(( $(date +%s) - _start ))

assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED after readiness poll"

# Should have taken >= 2s (the marker-write delay) but well below the
# start-timeout budget.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_elapsed" -ge 2 ] && [ "$_elapsed" -le 8 ]; then
    echo "OK: STARTED reached in ${_elapsed}s (poll waited for marker)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: elapsed=${_elapsed}s (expected 2-8s)"
fi

test_summary
