#!/bin/sh
# 215-journalctl-verbose-export — v2.1.0 1efd654: the verbose +
# export output formats. verbose prints one field per line with
# 4-space indent; export emits KEY=value lines, no indent, blank
# between events (systemd's export format).

wait_for_service "vesvc" "STARTED" 15
sleep 0.5

# --- verbose ---
_out=$(slinit-journalctl -u vesvc -o verbose -n 5 2>&1)
assert_contains "$_out" "    PRIORITY=" "verbose: PRIORITY indented"
assert_contains "$_out" "    UNIT=vesvc" "verbose: UNIT indented"
assert_contains "$_out" "    MESSAGE=" "verbose: MESSAGE indented"
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE '\[[0-9]+\.[0-9]{9}\]'; then
    echo "OK: verbose has [sec.ns] header"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: verbose missing [sec.ns] header"
fi

# --- export ---
_out=$(slinit-journalctl -u vesvc -o export -n 3 2>&1)
assert_contains "$_out" "__REALTIME_TIMESTAMP=" "export: __REALTIME_TIMESTAMP"
assert_contains "$_out" "__MONOTONIC_TIMESTAMP=" "export: __MONOTONIC_TIMESTAMP"
assert_contains "$_out" "MESSAGE=" "export: MESSAGE line"
# Export must have NO 4-space indented lines (that's verbose).
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE '^    [A-Z]'; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: export has verbose-indented lines"
else
    echo "OK: export has no verbose-style indentation"
fi

test_summary
