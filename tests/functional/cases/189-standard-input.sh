#!/bin/sh
# Test: standard-input-text + standard-input-data pipe payloads into
# the child's stdin.
# Validates: both services receive the configured payload on fd 0.
# For -data (base64 encoded), we check the decoded bytes reached the
# child, not the base64 form.

wait_for_service "sitext-svc" "STARTED" 10
wait_for_service "sidata-svc" "STARTED" 10

sleep 1

# standard-input-text: child dumps its stdin verbatim into
# /tmp/189-text-raw. Slinit writes the literal string (no newline),
# so the raw file should contain exactly the configured text.
_raw=$(cat /tmp/189-text-raw 2>/dev/null)
assert_eq "$_raw" "hello-from-slinit" "standard-input-text bytes reached the child's stdin"

# standard-input-data: child copied the raw bytes; slinit base64-
# decodes them before writing. "cGluZy1wYXlsb2Fk" decodes to
# "ping-payload".
_data=$(cat /tmp/189-data-out 2>/dev/null | tr -d '\0')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_data" in
"ping-payload"*)
    echo "OK: standard-input-data decoded and reached stdin"
    ;;
*)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: unexpected stdin bytes for standard-input-data: '$_data'"
    ;;
esac

test_summary
