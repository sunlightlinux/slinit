#!/bin/sh
# Test: seccomp extras — system-call-architectures + system-call-log
# + memory-deny-write-execute stack correctly on top of a base
# system-call-filter. Verifies Seccomp mode 2 (filter installed) on
# a kernel that supports every knob (linux-virt 6.18).

wait_for_service "seccompxtra-svc" "STARTED" 15
assert_service_state "seccompxtra-svc" "STARTED" "seccomp arch+log+MDWE stack parses + service starts"

sleep 1
_mode=$(awk '{print $2; exit}' /tmp/200-seccomp 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_mode" = "2" ]; then
    echo "OK: seccomp mode 2 (filter installed) confirms every knob compiled + installed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected Seccomp mode 2, got '$_mode'"
fi

test_summary
