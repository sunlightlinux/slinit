#!/bin/sh
# Test: post-start-command async execution.
# Validates:
#   1. post-start-command fires shortly after the service reaches STARTED.
#   2. post-start-command is asynchronous — it does NOT block the
#      service from promoting to STARTED. A hook that sleeps for
#      several seconds must not delay STARTED.
#
# Semantics under test (pkg/service/process.go runOneShotUp): after
# startProcess() succeeds, the hook is launched in a goroutine; the
# scheduler does not wait for it.

wait_for_service "poststart-svc" "STARTED" 10
assert_service_state "poststart-svc" "STARTED" "poststart-svc reaches STARTED"

# Give the async hook a moment to write its marker.
sleep 2

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/168-marker ]; then
    echo "OK: post-start marker present"
else
    echo "FAIL: post-start marker /tmp/168-marker missing"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Slow-hook service: STARTED must happen fast (well under the 4s the
# hook sleeps), and the marker must NOT yet be there when we check.
wait_for_service "poststart-slow-svc" "STARTED" 10
assert_service_state "poststart-slow-svc" "STARTED" "poststart-slow-svc reaches STARTED without waiting for hook"

# Confirm the hook is still sleeping — its marker cannot exist yet.
# There's some race between when we get here and when the hook writes,
# so allow a small window: check within 1 second of STARTED.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -f /tmp/168-slow-marker ]; then
    echo "OK: slow post-hook has not yet fired (STARTED promoted without waiting)"
else
    echo "FAIL: slow post-hook already ran — STARTED may have been blocked on it"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# After the sleep completes, the marker should appear. Give ample slack.
sleep 6
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f /tmp/168-slow-marker ]; then
    echo "OK: slow post-hook eventually completed and wrote its marker"
else
    echo "FAIL: slow post-hook marker never appeared"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
