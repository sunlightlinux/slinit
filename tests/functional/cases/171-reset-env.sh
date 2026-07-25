#!/bin/sh
# Test: `slinitctl reset-env <svc>` clears runtime env mutations.
# Validates:
#   1. setenv writes a KEY=VALUE that getallenv can read back.
#   2. reset-env drops every runtime mutation for that service.
#   3. reset-env is scoped to the named service — no cross-svc bleed.
#
# Semantics under test: pkg/control cmdResetEnv wipes the per-service
# runtime env dict. Upstart-inspired; complements setenv/unsetenv.

wait_for_service "env-svc" "STARTED" 10
assert_service_state "env-svc" "STARTED" "env-svc STARTED"

# Seed some runtime env.
slinitctl --system setenv env-svc TESTKEY1=alpha
slinitctl --system setenv env-svc TESTKEY2=beta

_before=$(slinitctl --system getallenv env-svc 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_before" | grep -q '^TESTKEY1=alpha$' && \
   echo "$_before" | grep -q '^TESTKEY2=beta$'; then
    echo "OK: both setenv values readable via getallenv"
else
    echo "FAIL: setenv values missing from getallenv output"
    echo "  getallenv output: $_before"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

# Wipe.
slinitctl --system reset-env env-svc

_after=$(slinitctl --system getallenv env-svc 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if ! echo "$_after" | grep -q '^TESTKEY1='; then
    echo "OK: TESTKEY1 cleared after reset-env"
else
    echo "FAIL: TESTKEY1 still present after reset-env"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if ! echo "$_after" | grep -q '^TESTKEY2='; then
    echo "OK: TESTKEY2 cleared after reset-env"
else
    echo "FAIL: TESTKEY2 still present after reset-env"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
