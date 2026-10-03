#!/bin/sh
# Test: chain-to, and the three conditions it actually depends on.
#
# The previous version of this case could not fail. It gave `boot` a
# `waits-for: chain-b` and then asserted that chain-b had started — which
# it would have whether chain-to worked or not. Worse, its source was a
# `scripted` service, and a scripted service can never satisfy the chain
# condition: chaining needs stop reason "terminated", meaning the process
# ended on its own, and a scripted service's start command completing
# makes it STARTED rather than stopped. So the case asserted a chain that
# the configuration could not produce, and passed.
#
# The real rule, from the code and matching dinit's: chain when
# always-chain is set, or when the service self-terminated AND exited 0
# AND is not about to restart. Here each clause gets a service, and no
# chain TARGET is in the boot graph — the chain is the only thing that can
# start them.
mark() { echo "MARK: $1" > /dev/console 2>/dev/null || true; }

# --- Clean self-termination chains ------------------------------------
mark "A: chain-a (process, exit 0) should chain to chain-b"
wait_for_service "chain-b" "STARTED" 15
assert_service_state "chain-b" "STARTED" "chain-b was started by the chain"
assert_eq "$(cat /tmp/chain-b-marker 2>/dev/null)" "chain-b-ran" "chain-b body ran"

# --- A non-zero exit does not chain -----------------------------------
# chain-c exits 3. Whether slinit records that as a start failure or as a
# non-clean termination, neither satisfies the condition — and the point
# of the assertion is that chain-d stays untouched either way.
mark "B: chain-c (exit 3) must not chain to chain-d"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e /tmp/chain-d-marker ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: chain-d ran — a non-zero exit must not chain"
else
    echo "OK: chain-d did not run (non-zero exit does not chain)"
fi
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$(svc_state chain-d)" = "STARTED" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: chain-d is STARTED despite its source exiting 3"
else
    echo "OK: chain-d is not started"
fi

# --- always-chain overrides the condition ------------------------------
# chain-e is scripted, so it cannot self-terminate; always-chain is the
# only way it chains, and it chains when it stops.
mark "C: stop chain-e, which has always-chain"
assert_service_state "chain-e" "STARTED" "chain-e is up before the stop"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e /tmp/chain-f-marker ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: chain-f ran before chain-e was stopped"
else
    echo "OK: chain-f has not run yet"
fi

timeout 10 slinitctl --system stop chain-e >/dev/null 2>&1
wait_for_service "chain-f" "STARTED" 15
assert_service_state "chain-f" "STARTED" "chain-f was started by always-chain on stop"
assert_eq "$(cat /tmp/chain-f-marker 2>/dev/null)" "chain-f-ran" "chain-f body ran"

mark "done: summary"
test_summary
