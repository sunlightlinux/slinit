#!/bin/sh
# Test: exec-condition (skip vs run) + exec-search-path smoke.
# Validates:
#   1. exec-condition = /bin/true → main command runs (marker exists).
#   2. exec-condition = /bin/false → svc SKIPPED; main command must
#      NOT run (marker absent). Skipped short-circuits to STARTED in
#      slinit, so `is-started` still returns 0, but the body of the
#      service is bypassed.
#   3. exec-search-path parses and doesn't break command resolution.

wait_for_service "ec-pass-svc" "STARTED" 10
wait_for_service "esp-svc" "STARTED" 10
# ec-skip-svc reaches STARTED (via SKIPPED short-circuit) but its
# command shouldn't have run. Give it time to settle either way.
sleep 3

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/190-ec-pass-ran ]; then
    echo "OK: exec-condition passed → main command ran"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /tmp/190-ec-pass-ran missing"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -f /tmp/190-ec-skip-ran ]; then
    echo "OK: exec-condition failed → main command skipped"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: main command ran despite exec-condition = /bin/false"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/190-esp-ran ]; then
    echo "OK: exec-search-path did not break command resolution"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /tmp/190-esp-ran missing — exec-search-path broke resolution"
fi

test_summary
