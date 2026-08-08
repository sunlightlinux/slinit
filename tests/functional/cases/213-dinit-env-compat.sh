#!/bin/sh
# 213-dinit-env-compat — v2.1.0 4eee317: dinit-parity sweep
# closes five env-var + bootstrap-path gaps. This case exercises
# the two operator-visible ones inside the guest:
#   1. Child services see SLINIT_SERVICENAME + SLINIT_SERVICEDSCDIR
#      injected into their env.
#   2. slinitctl honors DINIT_SOCKET_PATH as an alternative to
#      SLINIT_SOCKET_PATH / --socket-path.

wait_for_service "envsvc" "STARTED" 15
sleep 0.5

DUMP=/tmp/env-dump.txt
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -s "$DUMP" ]; then
    echo "OK: env-svc dumped $(wc -l < "$DUMP") env lines"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: env dump missing"
    test_summary; return
fi

# SLINIT_SERVICENAME must equal the service name.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q '^SLINIT_SERVICENAME=envsvc$' "$DUMP"; then
    echo "OK: SLINIT_SERVICENAME=envsvc injected"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: SLINIT_SERVICENAME missing"
    grep -E '^(DINIT|SLINIT)_' "$DUMP" | head
fi

# SLINIT_SERVICEDSCDIR points at the desc dir (/etc/slinit.d in
# the guest).
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q '^SLINIT_SERVICEDSCDIR=/etc/slinit.d$' "$DUMP"; then
    echo "OK: SLINIT_SERVICEDSCDIR=/etc/slinit.d injected"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: SLINIT_SERVICEDSCDIR missing"
fi

# --- slinitctl honors DINIT_SOCKET_PATH env ---
# Positive: good path → read-only cmd succeeds.
_out=$(DINIT_SOCKET_PATH=/run/slinit.socket slinitctl --system ls 2>&1)
assert_contains "$_out" "boot" "DINIT_SOCKET_PATH=good works"

# Negative: bogus path → clean error, not crash.
_out=$(DINIT_SOCKET_PATH=/nonexistent.sock slinitctl ls 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
*"dial"*|*"connect"*|*"no such"*)
    echo "OK: bogus DINIT_SOCKET_PATH errors cleanly" ;;
*)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: bogus DINIT_SOCKET_PATH didn't error ('$_out')" ;;
esac

test_summary
