#!/bin/sh
# 171-slinitctl-reset-failed — a service that hits its restart-limit
# lands in FAILED. `reset-failed` clears the mark + restart-limit
# counter so the next start is treated as a fresh attempt. Also tests
# the no-arg form which sweeps every failed service.

SVC="acceptance-test-resetfailed"

cleanup() { svc_remove "$SVC"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
# Exit immediately; restart-limit-count=2 + restart-delay=0 means it
# hits FAILED after 2 attempts within restart-limit-interval.
command = /bin/sh -c 'exit 1'
restart = on-failure
restart-delay = 0
restart-limit-count = 2
restart-limit-interval = 30
EOF

slinitctl --system --no-wait start "$SVC" >/dev/null 2>&1

# Wait for FAILED state.
_e=0
while [ "$_e" -lt 15 ]; do
    if slinitctl --system is-failed "$SVC" >/dev/null 2>&1; then
        break
    fi
    sleep 1; _e=$((_e + 1))
done

assert_exit_code "slinitctl --system is-failed $SVC" 0 \
    "svc reached FAILED after restart-limit exhausted"

# Reset-failed clears the mark.
slinitctl --system reset-failed "$SVC" >/dev/null
sleep 1
assert_exit_code "slinitctl --system is-failed $SVC" 1 \
    "svc no longer reports as failed after reset-failed"

test_summary
