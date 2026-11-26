#!/bin/sh
# Test: a hook that outruns hook-timeout is killed with its whole process
# group, not just its direct child.
#
# Hooks ran with no process group of their own, so the timeout killed
# only the shell: `sleep & sleep` left both sleeps running, reparented
# to PID 1 with nobody watching them, while the log said the hook had
# been killed. Unique durations let the case find exactly its own sleeps.

printf '%s\n' 'sleep 311.7 &' 'sleep 311.7' > /tmp/274-prestart.sh
printf '%s\n' 'sleep 312.3 &' 'sleep 312.3' > /tmp/274-prestop.sh

count_sleeps() {
    # busybox ps prints the command line in the last columns.
    ps -o pid,args 2>/dev/null | grep -c "[s]leep $1"
}

# pre-start-command: the timeout fails the start.
printf '%s\n' 'type = process' 'hook-timeout = 1' \
    'pre-start-command = /bin/sh /tmp/274-prestart.sh' \
    'command = /bin/sleep 120' 'restart = false' > /etc/slinit.d/test-hook-prestart

# pre-stop-hook: the timeout lets the stop go on.
printf '%s\n' 'type = process' 'hook-timeout = 1' \
    'pre-stop-hook = /bin/sh /tmp/274-prestop.sh' \
    'command = /bin/sleep 120' 'restart = false' > /etc/slinit.d/test-hook-prestop

slinitctl --system start test-hook-prestart >/dev/null 2>&1
wait_for_service test-hook-prestart STOPPED 10 || true
sleep 1
echo "pre-start sleeps left: $(count_sleeps 311.7)"
assert_eq "$(count_sleeps 311.7)" "0" "timed-out pre-start-command left no process behind"

slinitctl --system start test-hook-prestop >/dev/null 2>&1
wait_for_service test-hook-prestop STARTED 10 || true
assert_service_state test-hook-prestop STARTED "service with a pre-stop-hook starts"
slinitctl --system stop test-hook-prestop >/dev/null 2>&1
wait_for_service test-hook-prestop STOPPED 10 || true
assert_service_state test-hook-prestop STOPPED "the stop completes after the hook times out"
sleep 1
echo "pre-stop sleeps left: $(count_sleeps 312.3)"
assert_eq "$(count_sleeps 312.3)" "0" "timed-out pre-stop-hook left no process behind"

pkill -f 'sleep 31[12]\.' 2>/dev/null
test_summary
