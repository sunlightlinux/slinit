#!/bin/sh
# Test: watchdog-timeout kills unresponsive service.
# Validates: watchdog-timeout, automatic restart after watchdog expiry.
# The service sends the initial ready notification but does NOT send
# subsequent keepalives, so the watchdog should fire after 3s.
#
# The restart count is the probe: the service must have started at
# least twice. This used to print OK when it had started once and the
# log mentioned neither "watchdog armed" nor "watchdog disabled" — the
# exact picture of a watchdog-timeout that was never honoured — so it
# could not fail. Now the only way out other than restarts >= 2 is a
# SKIP, and only when slinit logged that it disabled this service's
# watchdog (pipe deadlines unsupported on the notification fd).

wait_for_service "watchdog-svc" "STARTED" 15

# Wait for watchdog to fire and service to restart.
# watchdog-timeout=3s + restart-delay=1s + startup time + margin
sleep 12

starts=$(wc -l < /tmp/watchdog-starts 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$starts" -ge 2 ]; then
    echo "OK: watchdog triggered restart (starts=$starts)"
else
    _log=$( (cat /run/slinit/catch-all.log 2>/dev/null; slinit-journalctl -n 400 --no-pager 2>/dev/null) \
        | grep "watchdog-svc")
    echo "  diag: slinit log lines for watchdog-svc:"
    echo "$_log" | sed 's/^/    /'
    case "$_log" in
        *"watchdog disabled"*)
            skip_case "watchdog disabled on this platform (pipe deadlines not supported)"
            ;;
        *)
            echo "FAIL: watchdog did not trigger a restart (starts=$starts)"
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            ;;
    esac
fi

# Service should still be running
_state=$(slinitctl --system status watchdog-svc 2>/dev/null | grep 'State:' | awk '{print $2}')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_state" in
    STARTED)
        echo "OK: watchdog-svc is STARTED"
        ;;
    *)
        echo "FAIL: watchdog-svc state is '$_state', expected STARTED"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        ;;
esac

test_summary
