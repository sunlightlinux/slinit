#!/bin/sh
# 217-journalctl-vacuum-flush — v2.1.10 Sprint 2: --vacuum-*
# runs journald.Vacuum in-process, --flush signals a spawned
# slinit-journald via the admin control socket. Both need a
# writable journal dir in the guest.

wait_for_service "boot" "STARTED" 15

DIR=/tmp/vac-journal
PID=/tmp/vac.pid
SOCK=/tmp/vac-events.sock
CTL=/tmp/vac.ctl
LOG=/tmp/vac-daemon.log

rm -rf "$DIR" "$PID" "$SOCK" "$CTL" "$LOG"
mkdir -p "$DIR"

# --- --vacuum-files direct-on-files (no daemon) ---
# Seed the dir with 4 files (current + 3 archived) with distinct
# mtimes so vacuum oldest-first pruning is deterministic.
_today=$(date -u +%Y-%m-%d)
touch "$DIR/${_today}.jsonl"
touch -t 202601010000 "$DIR/2025-01-01.oldest.jsonl"
touch -t 202606010000 "$DIR/2025-06-01.middle.jsonl"
touch -t 202610010000 "$DIR/2025-10-01.newest.jsonl"

_before=$(ls "$DIR" | wc -l)
assert_eq "$_before" "4" "seeded 4 files"

slinit-journalctl --vacuum-files=2 --directory="$DIR" >/dev/null 2>&1
_after=$(ls "$DIR" | wc -l)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_after" -le 3 ]; then
    echo "OK: vacuum-files=2 pruned to $_after"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: vacuum didn't prune ($_after files remain)"
fi

# Current file must survive (vacuum's exclude-current rule).
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$DIR/${_today}.jsonl" ]; then
    echo "OK: current-day file survived vacuum"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: current-day file was pruned"
fi

# --- --flush via admin socket ---
# Spin up a journald and signal it. The daemon logs 'flushed →'
# on the flush handler.
slinit-journald --format=jsonl --dir="$DIR" \
    --pid-file="$PID" --socket="$SOCK" --admin-socket="$CTL" \
    >/dev/null 2>"$LOG" &
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$PID" ] && kill -0 "$(cat "$PID")" 2>/dev/null; then
    echo "OK: daemon spawned (pid $(cat "$PID"))"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: daemon did not start"
    tail "$LOG"
    test_summary; return
fi

# The slinit-journalctl --flush client dials
# /run/slinit-journald.ctl by default. Symlink our test CTL there
# so the client finds it without needing a --admin-socket override.
ln -sf "$CTL" /run/slinit-journald.ctl

_out=$(slinit-journalctl --flush 2>&1)
assert_contains "$_out" "flush command sent" "--flush prints send confirmation"

sleep 0.3
assert_contains "$(cat "$LOG")" "flushed" "daemon logged the flush handler"

# --relinquish-var switches to volatile.
_out=$(slinit-journalctl --relinquish-var 2>&1)
assert_contains "$_out" "relinquish-var command sent" "--relinquish-var sends command"

sleep 0.3
assert_contains "$(cat "$LOG")" "relinquished /var" "daemon logged the relinquish handler"

kill "$(cat "$PID")" 2>/dev/null
rm -f /run/slinit-journald.ctl

test_summary
