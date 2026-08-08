#!/bin/sh
# 211-journalctl-kernel-events — v2.1.0 read /dev/kmsg from start
# so `-k / --dmesg` returns current-boot kernel messages. Combines
# with v2.1.0's convention that kernel events self-identify as
# unit=kernel and skip the userspace _PID bracket.

# -k must return SOMETHING on a real system (kernel always emits
# at boot; XFRM netlink socket / EXT4-fs journal init are typical
# early lines).
_out=$(slinit-journalctl -k -n 5 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
_lines=$(echo "$_out" | grep -c 'kernel:')
if [ "$_lines" -ge 1 ]; then
    echo "OK: -k returned $_lines kernel line(s)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -k returned no kernel lines"
    echo "$_out"
fi

# Kernel lines must NOT have [PID] brackets (v2.1.0 rule: kernel
# events skip the emitter-PID bracket that would be misleading).
_out=$(slinit-journalctl -k -n 3 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE 'kernel\[[0-9]+\]:'; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: kernel line shows [PID] bracket — should be bare 'kernel:'"
    echo "$_out" | grep 'kernel' | head -1
else
    echo "OK: kernel lines have no [PID] bracket"
fi

# --dmesg is the long form of -k.
_out=$(slinit-journalctl --dmesg -n 2 2>&1)
assert_contains "$_out" "kernel:" "--dmesg (long form) returns kernel events"

# Kernel events in JSON payload must have unit=kernel + transport=kernel.
_json=$(slinit-journalctl -k -o json -n 1 2>&1 | head -1)
assert_contains "$_json" "\"unit\":\"kernel\"" "kernel event JSON has unit=kernel"
assert_contains "$_json" "\"_transport\":\"kernel\"" "kernel event JSON has _transport=kernel"

test_summary
