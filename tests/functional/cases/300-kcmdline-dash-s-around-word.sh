#!/bin/sh
# Test: `-s auto -s` on the kernel command line boots normally.
#
# .d/kernel-args appends LILO's `auto` between two -s. In slinit -s
# selects system mode, the same variable as -m, and the PID-1 filtering
# of kernel words was skipped whenever it was set: a -s before the
# first naked word made `auto` a boot service, its load failed and
# slinit went to the rescue menu with a 60 s auto-reboot. The -s after
# `auto` is the order 299 covers. In dinit -s never stops that
# filtering; only -m and -o do, for the words after them. A boot that
# reaches the rescue menu never starts this script, so the case fails
# by timeout.

CATCH_ALL=/run/slinit/catch-all.log
_state() { slinitctl --system status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }

echo "PID 1 argv: $(tr "\0" " " < /proc/1/cmdline)"
wait_for_service boot STARTED 10
assert_eq "$(_state boot)" "STARTED" "boot started"
_out=$(timeout 5 slinitctl --system list 2>&1)
echo "--- list:"; echo "$_out"; echo "---"
assert_not_contains "$_out" "auto" "auto is not a service"
_log=$(cat "$CATCH_ALL" 2>/dev/null)
assert_not_contains "$_log" "Failed to load" "no boot service failed to load"

test_summary
