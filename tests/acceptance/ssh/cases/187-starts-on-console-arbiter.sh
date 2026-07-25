#!/bin/sh
# 187-starts-on-console-arbiter — `options = starts-on-console` marks
# a service as requesting exclusive console access during its STARTING
# phase. slinit's console arbiter grants it, then releases on
# transition to STARTED. Coexistence with a `runs-on-console` sibling
# is the interesting case — the arbiter must serialize them, not
# deadlock.
#
# End-to-end console arbitration on a live system is easier to reason
# about here than in the minimal QEMU fixture (196), where the console
# arbiter has no real interactive controller to yield to.

SVC_STARTS="acceptance-test-starts-console"
SVC_RUNS="acceptance-test-runs-console"

cleanup() { svc_remove "$SVC_STARTS" "$SVC_RUNS"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC_STARTS" <<EOF
type = process
options = starts-on-console
command = /bin/sh -c 'sleep 2; exec sleep 600'
restart = false
EOF

svc_deploy "$SVC_RUNS" <<EOF
type = process
options = runs-on-console
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

# Start starts-on-console first. It should acquire the console during
# STARTING, then release before STARTED (per the arbiter contract).
slinitctl --system start "$SVC_STARTS" >/dev/null
wait_for_service "$SVC_STARTS" "STARTED" 20
assert_service_state "$SVC_STARTS" "STARTED" \
    "starts-on-console svc reached STARTED (arbiter released the console)"

# Now the runs-on-console svc — this should start cleanly too because
# starts-on-console has released the console lock.
slinitctl --system start "$SVC_RUNS" >/dev/null
wait_for_service "$SVC_RUNS" "STARTED" 15
assert_service_state "$SVC_RUNS" "STARTED" \
    "runs-on-console svc started after starts-on-console released"

test_summary
