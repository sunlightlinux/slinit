#!/bin/sh
# 210-journalctl-b-shortcut — v2.1.0 added `-b` as the systemd
# shortcut for `--boot`. Both forms must produce equivalent output
# for the current boot (bare `-b` == `-b 0` == `--boot`).

# All three forms should succeed on a running system.
_out_short=$(slinit-journalctl -b -n 2 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_out_short" ] && ! echo "$_out_short" | grep -qi "error\|unknown"; then
    echo "OK: -b (bare) succeeded"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -b returned error: $_out_short"
fi

_out_zero=$(slinit-journalctl -b0 -n 2 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_out_zero" ] && ! echo "$_out_zero" | grep -qi "error\|unknown"; then
    echo "OK: -b0 (concatenated) succeeded"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: -b0 returned error: $_out_zero"
fi

_out_long=$(slinit-journalctl --boot -n 2 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_out_long" ] && ! echo "$_out_long" | grep -qi "error\|unknown"; then
    echo "OK: --boot (long form) succeeded"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --boot returned error: $_out_long"
fi

# --boot=<current-boot-id> should also succeed — verify by pulling
# the current boot ID from the JSON payload of any event.
_bootid=$(slinit-journalctl -o json -n 1 2>&1 \
            | grep -oE '"_boot_id":"[a-f0-9]+"' \
            | head -1 | cut -d'"' -f4)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_bootid" ] && [ ${#_bootid} -eq 32 ]; then
    echo "OK: extracted current boot ID ($_bootid)"
    _out=$(slinit-journalctl -b "$_bootid" -n 1 2>&1)
    assert_not_contains "$_out" "cross-boot" "-b <current-hex> accepted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not extract 32-hex boot ID from JSON"
fi

test_summary
