#!/bin/sh
# Test: slinit-check validates service config files.
# Validates: offline config linting, error detection, --online mode.

# Offline check of a valid service
output=$(slinit-check -d /etc/slinit.d boot 2>&1)
assert_contains "$output" "No problems found" "valid service passes check"

# Offline check with system dirs
output=$(slinit-check --system boot 2>&1)
rc=$?
# Should not crash (rc 0 or 1 depending on warnings)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$rc" -le 1 ]; then
    echo "OK: slinit-check --system runs without crash"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check --system exited with rc=$rc"
fi

# Online check (queries running daemon)
output=$(slinit-check --online boot 2>&1)
assert_contains "$output" "Checking service: boot" "online mode connects to daemon"

test_summary
