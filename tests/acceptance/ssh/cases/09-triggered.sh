#!/bin/sh
# 09-triggered — a triggered service goes STOPPED → STARTING (on start) and
# only reaches STARTED once `slinitctl trigger` fires. The service has no
# `command =` — slinit treats `type = triggered` as a rendezvous/marker, not
# a wrapper around a program.

SVC="acceptance-test-triggered"

trap 'svc_remove "$SVC"' EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = triggered
EOF

# Start with --no-wait (global flag, before the subcommand) so the call
# returns while the service is held in STARTING waiting for the trigger.
slinitctl --no-wait --system start "$SVC" >/dev/null 2>&1
sleep 1
assert_service_state "$SVC" "STARTING" "$SVC parked in STARTING"

# Fire the trigger — should transition to STARTED.
slinitctl --system trigger "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED after trigger"

test_summary
