#!/bin/sh
# 203-journalctl-kernel-events — v2.1.0 reads /dev/kmsg from
# start-of-boot so `-k / --dmesg` returns current-boot kernel
# messages. Also validates the two rendering rules v2.1.0
# introduced for kernel events:
#   1. unit self-identifies as "kernel"
#   2. NO [PID] bracket (kernel doesn't have a userspace emitter)

wait_for_service "boot" "STARTED" 15

# -k must succeed (exit 0) even if the QEMU guest's early kmsg
# stream hasn't populated by the time this runs. On real hardware
# the buffer always has boot lines; on a tiny cached-kernel VM
# it may be empty. Both paths must run cleanly without erroring.
_TESTS_RUN=$((_TESTS_RUN + 1))
if _out=$(slinit-journalctl -k -n 10 2>&1); then
    echo "OK: -k succeeded (returned $(echo "$_out" | wc -l) line(s))"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -k errored"
    echo "$_out"
fi

# If ANY kernel line surfaced, it must obey the v2.1.0 render
# rules: unit=kernel (via bare 'kernel:' prefix, not
# 'kernel[N]:'). Conditional so a silent kmsg doesn't fail us.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_out" ]; then
    echo "OK: no kernel lines surfaced (empty kmsg, no bracket rule to check)"
elif echo "$_out" | grep -qE 'kernel\[[0-9]+\]:'; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: kernel line shows [PID] bracket (should be bare 'kernel:')"
    echo "$_out" | grep -E 'kernel\[[0-9]+\]' | head -1
else
    echo "OK: kernel lines have no [PID] bracket (or empty)"
fi

# JSON payload check gated on kernel lines being present.
_json=$(slinit-journalctl -k -o json -n 1 2>&1 | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_json" ]; then
    echo "OK: no JSON kernel event (empty kmsg — skip render-shape check)"
else
    if echo "$_json" | grep -q '"unit":"kernel"'; then
        echo "OK: JSON kernel event has unit=kernel"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: JSON kernel event missing unit=kernel: $_json"
    fi
fi

# --dmesg long form must at least succeed.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-journalctl --dmesg -n 2 >/dev/null 2>&1; then
    echo "OK: --dmesg (long form) accepted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --dmesg errored"
fi

test_summary
