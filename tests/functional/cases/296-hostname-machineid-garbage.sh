#!/bin/sh
# Test: garbage in /etc/machine-id and /etc/hostname does not stop the
# boot or take PID 1 down.
#
# .d/rootfs seeds /etc/machine-id with text that is not 32 hex digits
# and /etc/hostname with a comment and a name that is no valid
# hostname. systemd treats a malformed machine-id like a missing one (a
# transient ID for this boot) and ignores an invalid static hostname.

_state() { slinitctl --system status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }

wait_for_service boot STARTED 10
assert_eq "$(_state boot)" "STARTED" "boot completes"

# Service events go through the journal emitter, which stamps every
# event with the boot and machine IDs: start and stop a service so
# events are certainly emitted after boot.
_out=$(timeout 10 slinitctl --system start ev-probe 2>&1); _rc=$?
assert_eq "$_rc" "0" "a service starts"
_out=$(timeout 10 slinitctl --system stop ev-probe 2>&1); _rc=$?
assert_eq "$_rc" "0" "and stops"
_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 1 2>/dev/null && timeout 5 slinitctl --system list >/dev/null 2>&1; then
    echo "OK: PID 1 alive and answering"
else
    echo "FAIL: PID 1 not answering"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

_h=$(hostname)
echo "hostname: $_h"
assert_not_contains "$_h" " " "the kernel hostname has no spaces"
assert_not_contains "$_h" "#" "the comment line did not become the hostname"

grep -a "machine\|hostname\|panic" /run/slinit/catch-all.log | cut -c1-250

test_summary
