#!/bin/sh
# Test: -m after a stray kernel word makes only the words after it
# services, as in dinit.
#
# .d/kernel-args appends `splash -m kcmd-target`. -m says the line was
# written for slinit, so kcmd-target, after it, is a boot service;
# `splash`, before it, is still a kernel word. The -m used to be lost
# behind `splash`, and the default boot started.

_state() { slinitctl --system status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }

echo "PID 1 argv: $(tr "\0" " " < /proc/1/cmdline)"
wait_for_service kcmd-target STARTED 10
assert_eq "$(_state kcmd-target)" "STARTED" "the word after -m was started"
_out=$(timeout 5 slinitctl --system list 2>&1)
echo "--- list:"; echo "$_out"; echo "---"
assert_not_contains "$_out" "boot" "the default boot was not started in its place"
assert_not_contains "$_out" "splash" "the word before -m is not a service"

test_summary
