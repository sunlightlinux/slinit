#!/bin/sh
# Test: a bad entry in the /etc/slinit.conf.d overlay directory fails
# only the service it overlays, with an error naming the overlay file
# (and line), and leaves PID 1 and the boot alone.
#
# .d/rootfs/etc/slinit.conf.d holds: ov-bad, an overlay with a syntax
# error on line 2; ov-dir, a directory where an overlay file is looked
# for; and ov-good, a valid overlay replacing its service's command
# (the control: overlays are read at all). None of the three is
# reachable from boot; each is started by name.

CATCH_ALL=/run/slinit/catch-all.log
_state() { slinitctl --system status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }

wait_for_service boot STARTED 10
assert_eq "$(_state boot)" "STARTED" "boot completes"

_out=$(timeout 10 slinitctl --system start ov-good 2>&1); _rc=$?
assert_eq "$_rc" "0" "the service with a valid overlay starts"
assert_eq "$(cat /run/ov-good.stamp 2>/dev/null)" "overlay-applied" "its overlay was applied"

_out=$(timeout 10 slinitctl --system start ov-bad 2>&1); _rc=$?
echo "start ov-bad: rc=$_rc: $_out"
assert_eq "$_rc" "1" "the service with a broken overlay fails to load"
_out=$(timeout 10 slinitctl --system start ov-dir 2>&1); _rc=$?
echo "start ov-dir: rc=$_rc: $_out"
assert_eq "$_rc" "1" "the service whose overlay is a directory fails to load"

sleep 1
_log=$(grep -a "ov-" "$CATCH_ALL" 2>/dev/null)
echo "--- catch-all:"; echo "$_log" | cut -c1-250; echo "---"
assert_contains "$_log" "/etc/slinit.conf.d/ov-bad:2" "the error names the overlay file and line"
_l=$(echo "$_log" | grep -a "ov-dir")
assert_contains "$_l" "/etc/slinit.conf.d/ov-dir" "the error names the overlay directory"

_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 1 2>/dev/null && timeout 5 slinitctl --system list >/dev/null 2>&1; then
    echo "OK: PID 1 alive and answering"
else
    echo "FAIL: PID 1 not answering"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
