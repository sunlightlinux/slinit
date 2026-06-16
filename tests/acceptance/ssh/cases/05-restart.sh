#!/bin/sh
# 05-restart — verify slinitctl restart cycles a service (new PID after).

SVC="acceptance-test-restart"

trap 'svc_remove "$SVC"' EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while true; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC initial STARTED"

_pid_before="$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')"

slinitctl --system restart "$SVC" >/dev/null 2>&1
# restart returns when the service is back up; still poll to absorb any
# transition lag.
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED after restart"

_pid_after="$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid_before" ] && [ -n "$_pid_after" ] \
    && [ "$_pid_before" != "$_pid_after" ]; then
    echo "OK: pid changed across restart ($_pid_before -> $_pid_after)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid did not change ($_pid_before -> $_pid_after)"
fi

test_summary
