#!/bin/sh
# 265-process-interrupted-start-escalates: a process service whose start is
# interrupted moves to STOPPING with the stop-timeout armed, as dinit does.
# slinit sent SIGINT and stayed STARTING with no stop-timeout: a stop of a
# start whose process ignores SIGINT left it STARTING, process alive, for good;
# and a process that closed its readiness fd and kept running stayed STARTING
# for good.

printf '#!/bin/sh\ntrap "" INT\nwhile :; do sleep 1; done\n' > /run/265-ignore-int.sh
printf '#!/bin/sh\nexec 3>&-\nwhile :; do sleep 1; done\n' > /run/265-close-ready.sh

slinitctl --system --no-wait start neverready > /dev/null 2>&1
wait_for_service "neverready" "STARTING" 5
slinitctl --system --no-wait stop neverready > /dev/null 2>&1
# stop-timeout 1s, then SIGKILL.
wait_for_service "neverready" "STOPPED" 8
assert_service_state "neverready" "STOPPED" "stop of a SIGINT-ignoring start escalated to SIGKILL"
assert_eq "$(ps | grep -c '[2]65-ignore-int')" "0" "its process is gone"

slinitctl --system --no-wait start closesready > /dev/null 2>&1
wait_for_service "closesready" "STOPPED" 8
assert_service_state "closesready" "STOPPED" "a closed readiness fd fails the start and brings the process down"
assert_eq "$(ps | grep -c '[2]65-close-ready')" "0" "its process is gone"

test_summary
