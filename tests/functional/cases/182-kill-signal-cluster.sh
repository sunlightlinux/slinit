#!/bin/sh
# Test: full kill / signal cluster smoke.
# Validates: kill-mode + final-kill-signal + restart-kill-signal +
# watchdog-signal + survive-final-kill-signal all parse and coexist
# on a running service. Behavioural per-signal verification lives in
# pkg/service unit tests; this catches regressions where the parser
# or loader drops a directive on the floor.

wait_for_service "killcluster-svc" "STARTED" 10
assert_service_state "killcluster-svc" "STARTED" "kill-signal cluster parses + service starts"

test_summary
