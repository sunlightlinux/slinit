#!/bin/sh
# 252-rm-dep-while-waiting: rm-dep of the dependency a service is waiting on
# releases the wait — the service starts on its remaining dependencies. It
# used to stay STARTING for good (dinit's rm_dep has the same gap).

slinitctl --system --no-wait start waiter > /dev/null 2>&1
wait_for_service "slowdep" "STARTING" 5
assert_service_state "waiter" "STARTING" "waiter waits for its slow dependency"

out=$(slinitctl --system rm-dep waiter depends-on slowdep 2>&1)
assert_contains "$out" "Removed" "rm-dep accepted"
wait_for_service "waiter" "STARTED" 5
assert_service_state "waiter" "STARTED" "waiter starts once the awaited dependency is removed"

slinitctl --system stop waiter > /dev/null 2>&1
slinitctl --system stop slowdep > /dev/null 2>&1
test_summary
