#!/bin/sh
# 204-slinit-supports — v2.1.0 e9b5213: the self-introspection
# CLI. Enumeration flags (--list-directives / --list-opcodes /
# --list-all) + direct lookup by name. Fires against a live
# slinit-supports so any drift between the compiled binary and
# doc/features.md surfaces here.

# Missing-arg → help message that names the three enumeration flags.
_out=$(slinit-supports 2>&1)
assert_contains "$_out" "--list-directives" "help mentions --list-directives"
assert_contains "$_out" "--list-opcodes" "help mentions --list-opcodes"
assert_contains "$_out" "--list-all" "help mentions --list-all"

# --list-directives covers the workhorses.
_out=$(slinit-supports --list-directives 2>&1)
assert_contains "$_out" "type" "directives include 'type'"
assert_contains "$_out" "command" "directives include 'command'"
assert_contains "$_out" "restart" "directives include 'restart'"
assert_contains "$_out" "depends-on" "directives include 'depends-on'"
assert_contains "$_out" "waits-for" "directives include 'waits-for'"

# --list-opcodes emits v7 wire commands.
_out=$(slinit-supports --list-opcodes 2>&1)
assert_contains "$_out" "CmdStartService" "opcodes include CmdStartService"
assert_contains "$_out" "CmdJournalQuery" "opcodes include CmdJournalQuery"
assert_contains "$_out" "CmdEnableServiceV7" "opcodes include the V7 enable atomic"

# Lookup by name returns non-empty descriptive text.
_out=$(slinit-supports command 2>&1)
assert_contains "$_out" "command" "lookup for 'command' surfaces info"

# Unknown name → clean error, exit non-zero.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinit-supports nonexistent-directive-xyz 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: unknown-name lookup should exit non-zero"
else
    echo "OK: unknown-name lookup exits non-zero"
fi

test_summary
