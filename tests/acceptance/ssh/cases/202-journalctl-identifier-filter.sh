#!/bin/sh
# 202-journalctl-identifier-filter — v2.1.6 Group A: -t / -T /
# --identifier / --exclude-identifier resolve identifier via the
# same chain the short renderer uses (SyslogIdentifier → Unit →
# Comm), so filtering on a slinit-emitted "STARTED" event's Unit
# name works even though no SyslogIdentifier was set.
#
# Also validates the v2.1.7 fix: -t combined with a small -n
# still surfaces the matching event (client-side re-filter after
# server bypass of the wire Limit).

# getty-tty1 is guaranteed to have emitted a "STARTED" event
# during boot; use its Unit as our probe identifier.
IDENT=getty-tty1

_out=$(slinit-journalctl -t "$IDENT" -n 3 2>&1)
assert_contains "$_out" "getty-tty1" "-t IDENT surfaces matching event"

# Small-limit regression guard: -n 1 with -t must NOT return empty
# even though the buffer's tail slice probably doesn't contain a
# getty-tty1 event.
_out=$(slinit-journalctl -t "$IDENT" -n 1 2>&1)
assert_contains "$_out" "getty-tty1" "-t IDENT -n 1 doesn't drop matches (v2.1.7 fix)"

# --exclude-identifier: kernel events are usually the loudest,
# excluding them and taking -n 5 should surface non-kernel entries.
_out=$(slinit-journalctl -T kernel -n 5 2>&1)
assert_not_contains "$_out" "kernel: Initializing XFRM" "-T excludes matching identifier"

# Nonsense identifier → no output (not an error).
_out=$(slinit-journalctl -t "def-nope-xyz-$$" -n 1 2>&1)
assert_eq "$_out" "" "-t unknown IDENT returns empty (not error)"

test_summary
