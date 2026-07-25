#!/bin/sh
# Test: the full restrict-* cluster stacks and installs cleanly.
# Validates:
#   1. Every arg-checking BPF program (RestrictRealtime, Namespaces,
#      SUIDSGID, FileSystems, AddressFamilies) compiles and installs;
#      any compilation error would cause slinit-runner to abort before
#      exec.
#   2. The child sees Seccomp: 2 (SECCOMP_MODE_FILTER) after all five
#      filters are stacked. Proves the seccomp path executed rather
#      than being silently skipped.
#
# Behavioural verification of each individual filter (that the covered
# syscalls actually EPERM) lives in pkg/seccomp/restrict_test.go; the
# functional test is a stack-and-run smoke.

wait_for_service "restrict-svc" "STARTED" 15
assert_service_state "restrict-svc" "STARTED" "restrict-svc STARTED with 5-filter stack"

sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -f /tmp/178-seccomp ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /tmp/178-seccomp missing — body did not run"
    test_summary
    return
fi

_mode=$(awk '{print $2; exit}' /tmp/178-seccomp)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_mode" = "2" ]; then
    echo "OK: child running under SECCOMP_MODE_FILTER (mode=2, filters stacked)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected Seccomp mode 2, got '$_mode'"
fi

test_summary
