#!/bin/sh
# Test: file-descriptor-store-preserve = yes|no|on-success parses
# and loads for every accepted value. Full runtime verification of
# the preserve semantics (fds retained across stop, cleared across
# stop, cleared on failure only) would require an sd_notify-aware
# helper that sends FDSTORE=1 with a real fd — not available in
# the busybox-only VM. Parse-and-start regression is the invariant
# this test protects.

for _svc in preserve-yes preserve-no preserve-on-success; do
    wait_for_service "$_svc" "STARTED" 10
    assert_service_state "$_svc" "STARTED" "$_svc reached STARTED"

    _pid=$(slinitctl --system status "$_svc" 2>/dev/null | awk '/PID:/ { print $2; exit }')
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ -n "$_pid" ] && [ "$_pid" != "0" ] && [ -d "/proc/$_pid" ]; then
        echo "OK: $_svc has live PID=$_pid"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $_svc has no live PID (got '$_pid')"
    fi
done

# Also verify NOTIFY_SOCKET is exported to each — fd-store requires
# it. This proves the parent-side wiring is up.
for _svc in preserve-yes preserve-no preserve-on-success; do
    _pid=$(slinitctl --system status "$_svc" 2>/dev/null | awk '/PID:/ { print $2; exit }')
    [ -z "$_pid" ] || [ ! -d "/proc/$_pid" ] && continue
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if tr '\0' '\n' < "/proc/$_pid/environ" 2>/dev/null | grep -q '^NOTIFY_SOCKET='; then
        echo "OK: $_svc has NOTIFY_SOCKET in its environment"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $_svc missing NOTIFY_SOCKET (fd-store not wired up)"
    fi
done

test_summary
