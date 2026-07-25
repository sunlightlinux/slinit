#!/bin/sh
# Test: start-timeout arms on the ready-check-command path.
# Validates:
#   1. A service whose ready-check never succeeds is force-stopped after
#      start-timeout expires — proves the timer is genuinely wired to the
#      ready-check watch loop (pkg/service/process.go ~line 2010).
#   2. The failure surfaces as `slinitctl is-failed` exiting 0.
#   3. A control service with a passing ready-check reaches STARTED
#      well within the same window — the timeout does not fire when the
#      readiness signal arrives first.

# Give both services enough time to run their full ready-check +
# timeout cycle. start-timeout=3, so 6s is generous slack.
sleep 6

# quick-svc's /bin/true ready-check should succeed on the first poll,
# well before the 3s cap.
assert_service_state "quick-svc" "STARTED" "quick-svc reaches STARTED via ready-check"

# stall-svc's /bin/false never succeeds; start-timeout must have fired.
_state=$(slinitctl --system status stall-svc 2>/dev/null | awk '/State:/ { print $2; exit }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_state" != "STARTED" ]; then
    echo "OK: stall-svc not STARTED (state: $_state)"
else
    echo "FAIL: stall-svc reached STARTED despite failing ready-check + start-timeout=3"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# The failure should be observable via `slinitctl is-failed`.
assert_exit_code "slinitctl --system is-failed stall-svc" 0 \
    "stall-svc reports as failed after start-timeout expiry"

test_summary
