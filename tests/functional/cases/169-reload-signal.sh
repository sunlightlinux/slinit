#!/bin/sh
# Test: `slinitctl reload-signal` + `reload-signal = SIG*` directive.
# Validates:
#   1. The declared signal is actually delivered to the service body
#      (visible via a SIGUSR1 trap that appends to a marker file).
#   2. A service WITHOUT reload-signal configured returns a clear
#      error when the operator tries to send it — no default signal
#      is inferred.
#
# Semantics under test:
#   - Directive `reload-signal = SIGUSR1` in pkg/config/parser.go maps
#     to record.reloadSignal.
#   - `slinitctl reload-signal svc` in cmd/slinitctl/main.go
#     (cmdReloadSignal) validates the directive is set and sends the
#     signal to the main pid via the control-socket signal path.

wait_for_service "reloadsig-svc" "STARTED" 10
assert_service_state "reloadsig-svc" "STARTED" "reloadsig-svc STARTED"
wait_for_service "nosig-svc" "STARTED" 10
assert_service_state "nosig-svc" "STARTED" "nosig-svc STARTED (control)"

# Baseline: no signal delivered yet.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -f /tmp/169-reload-hits ]; then
    echo "OK: no prior signal hits recorded before reload-signal"
else
    echo "FAIL: /tmp/169-reload-hits already present pre-test"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Send the declared signal (SIGUSR1) to reloadsig-svc.
slinitctl --system reload-signal reloadsig-svc
# The signal handler in busybox sh runs after the current sleep returns
# (up to 1s), so allow a small window.
sleep 2

_TESTS_RUN=$((_TESTS_RUN + 1))
_hits=$(wc -l < /tmp/169-reload-hits 2>/dev/null || echo 0)
if [ "$_hits" -ge 1 ]; then
    echo "OK: SIGUSR1 delivered to service body ($_hits hit(s))"
else
    echo "FAIL: no hits recorded after slinitctl reload-signal"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Send it a second time — accumulator should tick up.
slinitctl --system reload-signal reloadsig-svc
sleep 2

_TESTS_RUN=$((_TESTS_RUN + 1))
_hits2=$(wc -l < /tmp/169-reload-hits 2>/dev/null || echo 0)
if [ "$_hits2" -ge 2 ]; then
    echo "OK: second reload-signal delivered ($_hits2 total hits)"
else
    echo "FAIL: second reload-signal did not increment hit count ($_hits2)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Control service without reload-signal declared: slinitctl must
# refuse rather than silently sending a default signal.
_out=$(slinitctl --system reload-signal nosig-svc 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ] && echo "$_out" | grep -qi 'reload-signal'; then
    echo "OK: reload-signal on undeclared svc failed with a reload-signal-shaped error"
else
    echo "FAIL: reload-signal on undeclared svc did not fail cleanly (rc=$_rc out=$_out)"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
