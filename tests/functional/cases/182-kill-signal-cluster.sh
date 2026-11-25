#!/bin/sh
# Test: the kill/signal cluster parses, and restart-kill-signal is the
# signal a restart sends while a plain stop still sends SIGTERM.
# (The case used to check only that the service started.)

wait_for_service "killcluster-svc" "STARTED" 10
assert_service_state "killcluster-svc" "STARTED" "kill-signal cluster parses + service starts"
rm -f /run/182-sig

slinitctl --system restart killcluster-svc >/dev/null 2>&1
wait_for_service "killcluster-svc" "STARTED" 10
sleep 0.5
assert_eq "$(cat /run/182-sig 2>/dev/null)" "USR1" "restart sent restart-kill-signal (SIGUSR1)"

rm -f /run/182-sig
slinitctl --system stop killcluster-svc >/dev/null 2>&1
wait_for_service "killcluster-svc" "STOPPED" 10
assert_eq "$(cat /run/182-sig 2>/dev/null)" "TERM" "plain stop sent SIGTERM"

test_summary
