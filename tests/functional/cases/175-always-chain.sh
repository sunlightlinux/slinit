#!/bin/sh
# Test: `options = always-chain` fires chain-to even on failure.
# Validates:
#   1. A svc that exits non-zero WITH always-chain still triggers its
#      chain-to → the target starts.
#   2. A svc that exits non-zero WITHOUT always-chain does NOT trigger
#      chain-to → the target stays stopped. This proves the flag is
#      actually load-bearing.
#
# Semantics under test: pkg/service/record.go ~line 2795,
# shouldChain = AlwaysChain OR (clean-exit AND !willRestart).

# Both parents run `exit 1` immediately; the chain decision is made
# once they enter Stopped(). Give the scheduler a moment.
sleep 3

# always-chainer → always-target must be STARTED.
assert_service_state "always-target" "STARTED" "always-chain fires chain-to even after non-zero exit"

# normal-chainer → normal-target must NOT be STARTED (no chain fired).
_state=$(slinitctl --system status normal-target 2>/dev/null | awk '/State:/ { print $2; exit }')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_state" != "STARTED" ]; then
    echo "OK: normal-target not STARTED — chain suppressed on non-zero exit without always-chain (state: $_state)"
else
    echo "FAIL: normal-target STARTED — chain-to fired despite failure and no always-chain flag"
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
fi

test_summary
