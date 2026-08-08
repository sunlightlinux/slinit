#!/bin/sh
# 209-slinit-supports — v2.1.0: the slinit-supports CLI is the
# self-introspection surface (`doc/features.md` companion). Ships
# a lookup for any name (directive, opcode, feature) plus batch
# listings via --list-directives / --list-opcodes / --list-all.

# Missing argument path prints a clear usage hint mentioning the
# three --list flags so operators discover the enumeration paths.
_out=$(slinit-supports 2>&1)
assert_contains "$_out" "--list-directives" "help mentions --list-directives"
assert_contains "$_out" "--list-opcodes" "help mentions --list-opcodes"
assert_contains "$_out" "--list-all" "help mentions --list-all"

# --list-directives must include at least the workhorse ones we
# emit from converters (type, command, restart, waits-for).
_out=$(slinit-supports --list-directives 2>&1)
assert_contains "$_out" "type" "--list-directives includes 'type'"
assert_contains "$_out" "command" "--list-directives includes 'command'"
assert_contains "$_out" "restart" "--list-directives includes 'restart'"

# --list-opcodes emits every wire-protocol command name; check
# for a few we know exist across the v7 protocol.
_out=$(slinit-supports --list-opcodes 2>&1)
assert_contains "$_out" "CmdStartService" "--list-opcodes includes CmdStartService"
assert_contains "$_out" "CmdJournalQuery" "--list-opcodes includes CmdJournalQuery"

# Direct lookup by name → returns descriptive text (should include
# at least the name and 'directive' or 'opcode' label).
_out=$(slinit-supports command 2>&1)
assert_contains "$_out" "command" "lookup for 'command' surfaces info"

test_summary
