#!/bin/sh
# 205-slinitctl-analyze — v2.1.2 bedb280: the analyze subcommand
# dispatcher lands as `slinitctl analyze {time,blame,critical-
# chain,dot,plot}`. First four have live implementations; plot is
# a documented not-implemented stub because BootTime protocol
# exposes durations but not per-svc start timestamps.

wait_for_service "boot" "STARTED" 15

# time = boot-time summary (same underlying protocol call as
# `slinitctl boot-time`).
_out=$(slinitctl --system analyze time 2>&1)
assert_contains "$_out" "Startup finished" "analyze time prints startup summary"
assert_contains "$_out" "kernel" "analyze time names kernel phase"
assert_contains "$_out" "userspace" "analyze time names userspace phase"

# blame = per-svc durations, ms-granular.
_out=$(slinitctl --system analyze blame 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE '[0-9]+ms'; then
    echo "OK: analyze blame includes ms timings"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: analyze blame lacks ms timings"
fi

# critical-chain walks slowest dep path, ending at 'boot'.
_out=$(slinitctl --system analyze critical-chain 2>&1)
assert_contains "$_out" "boot" "analyze critical-chain includes boot"

# dot emits GraphViz digraph with at least one edge.
_out=$(slinitctl --system analyze dot 2>&1)
assert_contains "$_out" "digraph" "analyze dot emits GraphViz digraph"
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q -- "->"; then
    echo "OK: analyze dot includes an edge"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: analyze dot has no edges"
fi

# plot is a documented stub — must NOT crash + must name the
# workaround (analyze dot | dot -Tsvg).
_out=$(slinitctl --system analyze plot 2>&1)
assert_contains "$_out" "not implemented" "analyze plot documents itself as unimplemented"
assert_contains "$_out" "analyze dot" "stub message references analyze dot workaround"

# Unknown subcommand errors cleanly, names valid ones.
_out=$(slinitctl --system analyze bogus 2>&1)
assert_contains "$_out" "unknown subcommand" "unknown analyze subcommand errors cleanly"

test_summary
