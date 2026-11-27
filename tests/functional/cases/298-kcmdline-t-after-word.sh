#!/bin/sh
# Test: a -t after a stray kernel word is honoured.
#
# .d/kernel-args appends `splash -t kcmd-target`. slinit(8) says -t is
# always honoured; as PID 1 option parsing used to stop at the first
# naked word, so `splash` hid the -t and the default boot started
# instead. kcmd-target waits for the test runner like boot does, so
# this script runs either way and can tell which one was started.

_state() { slinitctl --system status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }

echo "PID 1 argv: $(tr "\0" " " < /proc/1/cmdline)"
wait_for_service kcmd-target STARTED 10
assert_eq "$(_state kcmd-target)" "STARTED" "the -t service after a stray word was started"
_out=$(timeout 5 slinitctl --system list 2>&1)
echo "--- list:"; echo "$_out"; echo "---"
assert_not_contains "$_out" "boot" "the default boot was not started in its place"
assert_not_contains "$_out" "splash" "the stray word is not a service"

test_summary
