#!/bin/sh
# Test: chain-to causes service B to start when service A stops cleanly.
# Validates: ChainTo config option, automatic chaining on clean exit.

# Both services should have started (chain-a runs, chains to chain-b)
sleep 3

# chain-a should have run
assert_eq "$(cat /tmp/chain-a-marker 2>/dev/null)" "chain-a-ran" "chain-a command executed"

# chain-b should have been started via chain-to
assert_eq "$(cat /tmp/chain-b-marker 2>/dev/null)" "chain-b-ran" "chain-b started via chain-to"

# chain-b should be in STARTED state (scripted service that succeeded)
assert_service_state "chain-b" "STARTED" "chain-b is STARTED"

test_summary
