#!/bin/sh
# 157-wait-timeout — -w / --wait SEC (runit `sv -w`) caps how long
# slinitctl waits for the daemon's reply. Verifies:
#   1. `-w 5 list` succeeds against a healthy daemon;
#   2. `-w=5 list` (equals form) works too;
#   3. bad values (non-integer, negative) exit non-zero with a
#      diagnostic before the socket is even touched.

# Happy path — long enough that no timeout can fire on a healthy VM.
_out=$(slinitctl --system -w 5 list 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: -w 5 list rc=0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -w 5 list rc=$_rc, output=$_out"
fi

# Equals form.
_out=$(slinitctl --system --wait=5 list 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: --wait=5 list rc=0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --wait=5 list rc=$_rc, output=$_out"
fi

# Bad value — non-integer must be rejected at flag-parse time.
_out=$(slinitctl --system -w abc list 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ] && echo "$_out" | grep -qi 'must be'; then
    echo "OK: -w abc rejected: $_out"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -w abc unexpectedly accepted (rc=$_rc)"
fi

# Negative — same error surface.
_out=$(slinitctl --system -w -1 list 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: -w -1 rejected (rc=$_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -w -1 unexpectedly accepted"
fi

test_summary
