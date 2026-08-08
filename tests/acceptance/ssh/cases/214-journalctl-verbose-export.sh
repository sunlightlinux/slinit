#!/bin/sh
# 214-journalctl-verbose-export — v2.1.0 added the verbose + export
# output formats. verbose prints one field per line prefixed by
# name; export emits `KEY=value` lines with a blank between events
# (systemd's export format).

# Deploy a synthetic service so we can drive a known event
# lifecycle instead of scraping the real boot journal (which is
# noisy + timing-dependent).
SVC="acceptance-test-fmtprobe"
cleanup() { svc_remove "$SVC"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<'EOF'
type = process
command = /bin/sh -c 'exec sleep 300'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10
sleep 0.5

# verbose: field-per-line with 4-space indent + KEY=VALUE.
_out=$(slinit-journalctl -u "$SVC" -o verbose -n 5 2>&1)
assert_contains "$_out" "    PRIORITY=" "verbose: PRIORITY on its own line"
assert_contains "$_out" "    UNIT=$SVC" "verbose: UNIT=SVC on its own line"
assert_contains "$_out" "    MESSAGE=" "verbose: MESSAGE on its own line"
# verbose headers include the RFC3339 timestamp + [sec.ns] block.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE '\[[0-9]+\.[0-9]{9}\]'; then
    echo "OK: verbose has [sec.ns] timestamp header"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no [sec.ns] header in verbose output"
fi

# export: KEY=VALUE lines, no indent, blank between events.
_out=$(slinit-journalctl -u "$SVC" -o export -n 3 2>&1)
assert_contains "$_out" "__REALTIME_TIMESTAMP=" "export: __REALTIME_TIMESTAMP present"
assert_contains "$_out" "__MONOTONIC_TIMESTAMP=" "export: __MONOTONIC_TIMESTAMP present"
assert_contains "$_out" "MESSAGE=" "export: MESSAGE present"
# export must NOT indent fields (that's verbose's convention).
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE '^    [A-Z]'; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: export has 4-space-indented lines (verbose leak)"
else
    echo "OK: export has no verbose-style indentation"
fi

test_summary
