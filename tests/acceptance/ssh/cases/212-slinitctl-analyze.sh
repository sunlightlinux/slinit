#!/bin/sh
# 212-slinitctl-analyze — v2.1.2 recovery+boot refactor landed
# five analyze subcommands under `slinitctl analyze`: time,
# blame, critical-chain, dot, plot. Together they replace what
# used to be a separate `slinit-analyze` binary + match systemd-
# analyze's operator surface.

# `time` = boot-time summary (equivalent to `slinitctl boot-time`).
_out=$(slinitctl analyze time 2>&1)
assert_contains "$_out" "Startup finished" "analyze time prints startup summary"

# `blame` = per-service startup durations, slowest first.
_out=$(slinitctl analyze blame 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE '[0-9]+ms'; then
    echo "OK: analyze blame includes per-svc ms timings"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: analyze blame lacks ms timings"
fi

# `critical-chain` walks the slowest dependency path from boot.
# Output should mention `boot` as the terminal node.
_out=$(slinitctl analyze critical-chain 2>&1)
assert_contains "$_out" "boot" "analyze critical-chain includes 'boot' node"

# `dot` emits GraphViz — must start with `digraph`.
_out=$(slinitctl analyze dot 2>&1)
assert_contains "$_out" "digraph" "analyze dot emits GraphViz digraph"
# Every edge is `svc1 -> svc2` — check at least one arrow present.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -q -- "->"; then
    echo "OK: analyze dot includes at least one edge"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no edges in analyze dot output"
fi

# `plot` is a documented not-implemented stub — the BootTime
# protocol exposes durations but not per-svc start timestamps,
# which SVG timeline layout needs. The stub prints a helpful
# fallback pointing at `analyze dot | dot -Tsvg`. Test the stub
# message rather than SVG output (the stub is the current
# contract until the protocol grows the timestamps).
_out=$(slinitctl analyze plot 2>&1)
assert_contains "$_out" "not implemented" "analyze plot documents itself as unimplemented"
assert_contains "$_out" "analyze dot" "stub message directs operator to analyze dot workaround"

# Unknown subcommand → clean error naming the valid options.
_out=$(slinitctl analyze bogus-op 2>&1)
assert_contains "$_out" "unknown subcommand" "unknown analyze subcommand errors cleanly"

test_summary
