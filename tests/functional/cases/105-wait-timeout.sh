#!/bin/sh
# Test: -w / --wait SEC (runit `sv -w`) caps how long slinitctl waits
# for the daemon's reply. Happy path succeeds; bad values reject at
# flag-parse time before the socket is even touched.

_out=$(slinitctl --system -w 5 list 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: -w 5 list rc=0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -w 5 list rc=$_rc, output=$_out"
fi

_out=$(slinitctl --system --wait=5 list 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: --wait=5 list rc=0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --wait=5 list rc=$_rc, output=$_out"
fi

_out=$(slinitctl --system -w abc list 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ] && echo "$_out" | grep -qi 'must be'; then
    echo "OK: -w abc rejected: $_out"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -w abc unexpectedly accepted (rc=$_rc)"
fi

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
