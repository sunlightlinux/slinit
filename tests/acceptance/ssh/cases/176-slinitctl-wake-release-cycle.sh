#!/bin/sh
# 176-slinitctl-wake-release-cycle — `wake` starts a service WITHOUT
# marking it active; `release` un-marks a previously active service
# (which stops it if no dependent needs it). Verifies both verbs work
# distinctly from plain `start`/`stop`.

SVC_TARGET="acceptance-test-wakerelease-target"
SVC_PARENT="acceptance-test-wakerelease-parent"

cleanup() { svc_remove "$SVC_PARENT" "$SVC_TARGET"; }
trap cleanup EXIT INT TERM

# `wake` requires the target to have at least one dependent that has
# already been started (otherwise slinit reports "no active
# dependents, cannot wake"). Set up a parent that waits-for the
# target, then `start` the parent to make the target
# dependency-active — that opens the window in which `wake` is legal.
svc_deploy "$SVC_TARGET" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

svc_deploy "$SVC_PARENT" <<EOF
type = process
waits-for: $SVC_TARGET
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

# Starting the parent brings the target up transitively.
slinitctl --system start "$SVC_PARENT" >/dev/null
wait_for_service "$SVC_TARGET" "STARTED" 10
assert_service_state "$SVC_TARGET" "STARTED" "target STARTED via parent waits-for"

# `release` un-marks active. Because the parent still waits-for it,
# the target stays STARTED — release only stops when nobody needs it.
slinitctl --system release "$SVC_TARGET" >/dev/null
sleep 1
assert_service_state "$SVC_TARGET" "STARTED" \
    "target stays STARTED after release while parent still waits-for it"

# Stop parent → target's dependents drop to zero → release-able.
slinitctl --system stop "$SVC_PARENT" >/dev/null
wait_for_service "$SVC_PARENT" "STOPPED" 10
wait_for_service "$SVC_TARGET" "STOPPED" 10
assert_service_state "$SVC_TARGET" "STOPPED" \
    "target STOPPED after parent stops (no more dependents)"

# `wake` on a target whose parent is running again brings it up
# without marking it active.
slinitctl --system start "$SVC_PARENT" >/dev/null
wait_for_service "$SVC_TARGET" "STARTED" 10
slinitctl --system stop "$SVC_TARGET" >/dev/null 2>&1 || true
sleep 1

# Now wake — parent is active dep, so wake is legal.
slinitctl --system wake "$SVC_TARGET" >/dev/null 2>&1
wait_for_service "$SVC_TARGET" "STARTED" 10
assert_service_state "$SVC_TARGET" "STARTED" "target STARTED after wake"

test_summary
