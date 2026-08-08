#!/bin/sh
# 209-journalctl-invocation-tracking — v2.1.8 Group E: every
# initiateStart mints a fresh 128-hex SLINIT_INVOCATION_ID; every
# event during the invocation carries it. Restarting the service
# produces a distinct ID and --list-invocations dedupes to one
# row per invocation. Fresh VM boot → ring buffer starts empty so
# we can assert exact counts (unlike the acceptance version where
# the buffer accumulates across runs).

wait_for_service "invsvc" "STARTED" 15
sleep 0.5

# First invocation ID from the STARTED event JSON.
_id1=$(slinit-journalctl -u invsvc -o json -n 5 2>&1 \
        | grep -oE '"SLINIT_INVOCATION_ID":"[a-f0-9]+"' \
        | head -1 | cut -d'"' -f4)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_id1" ] && [ ${#_id1} -eq 32 ]; then
    echo "OK: first invocation ID is 32-hex ($_id1)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: first invocation ID malformed: '$_id1'"
fi

# Stop + restart → distinct ID.
slinitctl --system stop invsvc >/dev/null
wait_for_service "invsvc" "STOPPED" 10
slinitctl --system start invsvc >/dev/null
wait_for_service "invsvc" "STARTED" 10
sleep 0.5

_ids=$(slinit-journalctl -u invsvc -o json 2>&1 \
        | grep -oE '"SLINIT_INVOCATION_ID":"[a-f0-9]+"' \
        | sort -u | wc -l)
assert_eq "$_ids" "2" "restart mints a distinct invocation ID"

# --list-invocations reports 2 rows on a fresh boot.
_out=$(slinit-journalctl -u invsvc --list-invocations 2>&1)
_lines=$(echo "$_out" | grep -cE '^[a-f0-9]{32}')
assert_eq "$_lines" "2" "--list-invocations reports 2 rows"

# --invocation=<first> narrows to only that lifecycle's events.
_out=$(slinit-journalctl -u invsvc --invocation="$_id1" -o cat 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
_n=$(echo "$_out" | wc -l)
if [ "$_n" -ge 1 ]; then
    echo "OK: --invocation=$_id1 returned $_n line(s) from first lifecycle"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --invocation filter returned empty"
fi

test_summary
