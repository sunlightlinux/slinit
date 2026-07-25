#!/bin/sh
# 176-slinitctl-wake-release-cycle — `wake` starts a service WITHOUT
# marking it active; `release` un-marks a previously active service
# (which stops it if no dependent needs it). Verifies both verbs work
# distinctly from plain `start`/`stop`.

SVC="acceptance-test-wakerelease"

cleanup() { svc_remove "$SVC"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

# `wake` should bring the svc to STARTED but leave it not-active
# (release-able without dependent tracking).
slinitctl --system wake "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10
assert_service_state "$SVC" "STARTED" "svc STARTED after wake"

# `release` should stop the svc since no dependent needs it.
slinitctl --system release "$SVC" >/dev/null
wait_for_service "$SVC" "STOPPED" 10
assert_service_state "$SVC" "STOPPED" "svc STOPPED after release"

# `start` marks active — a plain `stop` should also bring it down.
slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10
assert_service_state "$SVC" "STARTED" "svc STARTED after explicit start"

# But a `release` should also work on an actively-started svc.
slinitctl --system release "$SVC" >/dev/null
wait_for_service "$SVC" "STOPPED" 10
assert_service_state "$SVC" "STOPPED" "svc STOPPED after release even when started active"

test_summary
