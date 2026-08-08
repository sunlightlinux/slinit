#!/bin/sh
# 180-slinitctl-shutdown-scheduled — schedule a shutdown far enough in
# the future that we can inspect `--status` and cancel before it fires.
# Exercises the operator round-trip without ever losing the VM.

# 180 minutes gives us plenty of margin; we cancel within seconds.
_when="180"

cleanup() {
    slinitctl --system shutdown -c >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# --- schedule ----------------------------------------------------------
slinitctl --system shutdown reboot "+$_when" >/dev/null 2>&1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $? -eq 0 ]; then
    echo "OK: scheduled shutdown reboot +$_when"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not schedule shutdown"
    test_summary
    return
fi

# --- status reports the scheduled shutdown -----------------------------
_status=$(slinitctl --system shutdown --status 2>&1)
assert_contains "$_status" "reboot" "shutdown --status mentions the scheduled type"

# --- cancel ------------------------------------------------------------
slinitctl --system shutdown -c >/dev/null
_status=$(slinitctl --system shutdown --status 2>&1)
assert_not_contains "$_status" "reboot" "after -c, no pending reboot in status"

test_summary
