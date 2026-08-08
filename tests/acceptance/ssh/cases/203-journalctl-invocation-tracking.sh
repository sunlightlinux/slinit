#!/bin/sh
# 203-journalctl-invocation-tracking — v2.1.8 Group E: every
# initiateStart mints a fresh 128-bit hex SLINIT_INVOCATION_ID
# and every event during that lifecycle carries it. Restarting a
# service produces a distinct ID, and --list-invocations dedupes
# to one row per invocation with (id, first_ts, last_ts).

SVC="acceptance-test-invocation"
cleanup() { svc_remove "$SVC"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<'EOF'
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

# First invocation.
slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10
sleep 0.5

# Read the invocation ID from the STARTED event's JSON payload.
_id1=$(slinit-journalctl -u "$SVC" -o json -n 5 2>&1 \
        | grep -oE '"SLINIT_INVOCATION_ID":"[a-f0-9]+"' \
        | head -1 | cut -d'"' -f4)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_id1" ] && [ ${#_id1} -eq 32 ]; then
    echo "OK: first invocation ID present + 32-hex ($_id1)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: first invocation ID missing or wrong length: '$_id1'"
fi

# Stop + restart → new invocation ID.
slinitctl --system stop "$SVC" >/dev/null
wait_for_service "$SVC" "STOPPED" 10
slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10
sleep 0.5

# Grab the second invocation ID from the most recent STARTED event.
# slinit's ring buffer accumulates across runs, so we don't compare
# exact counts — just verify the second run's ID is present AND
# distinct from _id1.
_id2=$(slinit-journalctl -u "$SVC" -o json -n 3 2>&1 \
        | grep -oE '"SLINIT_INVOCATION_ID":"[a-f0-9]+"' \
        | tail -1 | cut -d'"' -f4)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_id2" ] && [ "$_id2" != "$_id1" ]; then
    echo "OK: restart mints a distinct SLINIT_INVOCATION_ID ($_id2)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: second invocation ID missing or matches first ('$_id2' vs '$_id1')"
fi

# --list-invocations must report at least our two IDs. The buffer
# probably also carries invocations from previous test runs; that's
# expected — assert ≥ 2 rows AND both of our IDs are present.
_out=$(slinit-journalctl -u "$SVC" --list-invocations 2>&1)
_lines=$(echo "$_out" | grep -cE '^[a-f0-9]{32}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_lines" -ge 2 ]; then
    echo "OK: --list-invocations reports ≥ 2 rows ($_lines)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --list-invocations expected ≥ 2 rows, got $_lines"
fi
assert_contains "$_out" "$_id1" "--list-invocations includes first invocation"
assert_contains "$_out" "$_id2" "--list-invocations includes second invocation"

test_summary
