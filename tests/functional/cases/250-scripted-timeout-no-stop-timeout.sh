#!/bin/sh
# 250-scripted-timeout-no-stop-timeout: a timed-out scripted start command gets
# SIGINT; with stop-timeout = 0 (what TimeoutStopSec=0|infinity converts to)
# there is no stop-timeout to escalate on, and a command ignoring SIGINT kept
# the service STOPPING for good — and a shutdown with it. The SIGKILL now comes
# one more start-timeout after the SIGINT.

printf '#!/bin/sh\ntrap "" INT\nwhile :; do sleep 1; done\n' > /run/250-start.sh
slinitctl --system --no-wait start stubborn > /dev/null 2>&1
wait_for_service "stubborn" "STARTING" 5

# start-timeout 2s + one more 2s for the SIGINT to be honoured, plus slack.
wait_for_service "stubborn" "STOPPED" 12
assert_service_state "stubborn" "STOPPED" "timed-out start ends despite stop-timeout = 0"
assert_eq "$(ps | grep -c '[2]50-start.sh')" "0" "start command was killed"

slinitctl --system is-failed stubborn > /dev/null 2>&1
assert_eq "$?" "0" "a timed-out start is reported failed"

test_summary
