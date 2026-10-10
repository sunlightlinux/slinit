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
#
# The probe event is one this case emits. It used to be
# getty-tty1's boot-time STARTED, which made the case depend on
# boot state that a busy target no longer has: PID 1's journal is
# a 4096-event ring, and a perf-suite run evicts every boot event.
# Measured on ceres after one such run — `-t getty-tty1` returned
# nothing, the ring held exactly 4096 entries, and the oldest was
# a perf throwaway service from 95 minutes after boot. The case
# then failed on every run until the box rebooted, which reads as
# flakiness and is not: it is deterministic, just invisible until
# something fills the ring. Emitting our own event tests the same
# resolution chain (a slinit STARTED event with no
# SyslogIdentifier, resolved via Unit) without that dependency.

IDENT="${ACCEPTANCE_NS_PREFIX}jident"

cleanup() {
    svc_remove "$IDENT"
}
trap cleanup EXIT INT TERM

svc_deploy "$IDENT" <<EOF
type = scripted
command = /bin/true
EOF

slinitctl --system start "$IDENT" >/dev/null 2>&1
# A scripted service stays STARTED once its command has succeeded — it
# does not fall back to STOPPED, which an earlier version of this comment
# claimed. Either way the case does not depend on the state: what matters
# is that starting it emitted a STARTED event carrying its Unit name,
# which is what the identifier chain has to resolve.
_e=0
while [ "$_e" -lt 10 ]; do
    slinit-journalctl -t "$IDENT" -n 5 2>/dev/null | grep -q "$IDENT" && break
    sleep 1
    _e=$((_e + 1))
done

_out=$(slinit-journalctl -t "$IDENT" -n 3 2>&1)
assert_contains "$_out" "$IDENT" "-t IDENT surfaces matching event"

# Small-limit regression guard: -n 1 with -t must NOT return empty
# even though the buffer's tail slice probably doesn't contain our
# event. This is the v2.1.7 client-side re-filter.
_out=$(slinit-journalctl -t "$IDENT" -n 1 2>&1)
assert_contains "$_out" "$IDENT" "-t IDENT -n 1 doesn't drop matches (v2.1.7 fix)"

# --exclude-identifier is the negative of the same chain: asking for
# everything except our identifier must not return our event. The old
# assertion excluded `kernel` and looked for a boot-time XFRM line, so
# once the ring had turned over it passed without testing anything.
_out=$(slinit-journalctl -T "$IDENT" -n 20 2>&1)
assert_not_contains "$_out" "$IDENT" "-T IDENT excludes matching identifier"

# Nonsense identifier → no output (not an error).
_out=$(slinit-journalctl -t "def-nope-xyz-$$" -n 1 2>&1)
assert_eq "$_out" "" "-t unknown IDENT returns empty (not error)"

test_summary
