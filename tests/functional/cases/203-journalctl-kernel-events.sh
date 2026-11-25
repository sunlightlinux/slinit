#!/bin/sh
# 203-journalctl-kernel-events — v2.1.0 reads /dev/kmsg from
# start-of-boot so `-k / --dmesg` returns current-boot kernel
# messages. Also validates the two rendering rules v2.1.0
# introduced for kernel events:
#   1. unit self-identifies as "kernel"
#   2. NO [PID] bracket (kernel doesn't have a userspace emitter)

wait_for_service "boot" "STARTED" 15

# -k must succeed and return kernel lines: slinit reads /dev/kmsg from
# the start of boot, and the kernel always logs during boot, so an empty
# result means the kmsg reader did not run. (Empty output used to count
# as OK, which made every check below pass without a kernel line.)
_out=$(slinit-journalctl -k -n 10 2>/dev/null)
_rc=$?
_n=$(echo "$_out" | grep -c .)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" = 0 ] && [ "$_n" -ge 1 ]; then
    echo "OK: -k returned $_n kernel line(s)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -k rc=$_rc returned $_n line(s)"
fi

# Kernel lines render as bare 'kernel:', not 'kernel[N]:'.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE 'kernel\[[0-9]+\]:'; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: kernel line shows [PID] bracket (should be bare 'kernel:')"
    echo "$_out" | grep -E 'kernel\[[0-9]+\]' | head -1
else
    echo "OK: kernel lines have no [PID] bracket"
fi

_json=$(slinit-journalctl -k -o json -n 1 2>/dev/null | head -1)
assert_contains "$_json" '"unit":"kernel"' "JSON kernel event has unit=kernel"

# --dmesg long form must at least succeed.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-journalctl --dmesg -n 2 >/dev/null 2>&1; then
    echo "OK: --dmesg (long form) accepted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --dmesg errored"
fi

test_summary
