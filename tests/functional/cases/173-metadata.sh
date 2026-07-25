#!/bin/sh
# Test: author / version / usage directives surface in `slinitctl status`.
# Validates:
#   1. Each directive is echoed on its own labeled line ("Author:",
#      "Version:", "Usage:") with the exact value from the service file.
#   2. Absent directives produce no phantom lines — the control svc
#      must NOT show any of those labels.
#
# Semantics under test: cmd/slinitctl/main.go status rendering gates
# each label on the metadata field being non-empty.

wait_for_service "meta-svc" "STARTED" 10
wait_for_service "bare-svc" "STARTED" 10

_out=$(slinitctl --system status meta-svc 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q '^\s*Author:.*jarvis@example.org'; then
    echo "OK: Author surfaces in status"
else
    echo "FAIL: Author missing or wrong in status output"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q '^\s*Version:.*3.14.15'; then
    echo "OK: Version surfaces in status"
else
    echo "FAIL: Version missing or wrong in status output"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q '^\s*Usage:.*meta-svc --help'; then
    echo "OK: Usage surfaces in status"
else
    echo "FAIL: Usage missing or wrong in status output"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Control: bare-svc must NOT print any of these labels.
_bare=$(slinitctl --system status bare-svc 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if ! echo "$_bare" | grep -qE '^\s*(Author|Version|Usage):'; then
    echo "OK: no phantom metadata lines on bare-svc"
else
    echo "FAIL: bare-svc status shows metadata lines despite no directives"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
