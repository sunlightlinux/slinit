#!/bin/sh
# 11-rc-service — slinit's OpenRC-compatible rc-service wrapper must drive
# the same lifecycle as slinitctl: start, status, stop, -e exists, -r resolve.

SVC="acceptance-test-rc-svc"

trap 'svc_remove "$SVC"' EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

# start
rc-service "$SVC" start >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "rc-service start brought $SVC up"

# -e is mapped to slinitctl is-started, so it returns 0 only for STARTED
# services (not the strict OpenRC "is on disk" sense — slinit's loader
# requires a service to be loaded before it answers questions about it).
assert_exit_code "rc-service -e $SVC" 0 "rc-service -e on STARTED returns 0"

# status output should mention STARTED
_st="$(rc-service "$SVC" status 2>&1)"
assert_contains "$_st" "STARTED" "rc-service status reports STARTED"

# stop
rc-service "$SVC" stop >/dev/null 2>&1
wait_for_service "$SVC" "STOPPED" 10 || true
assert_service_state "$SVC" "STOPPED" "rc-service stop brought $SVC down"

test_summary
