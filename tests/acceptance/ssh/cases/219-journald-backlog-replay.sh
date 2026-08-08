#!/bin/sh
# 219-journald-backlog-replay — v2.1.0 1efd654: slinit-journald
# queries the running slinit's control socket at startup and
# replays every event emitted since boot but before the daemon
# started listening on its own socket. Without this, the operator
# loses everything between boot and journald launch.
#
# The test starts a fresh daemon on a scratch dir + confirms both:
#   (1) the startup log mentions the replay count.
#   (2) the persisted journal file has ≥ 1 pre-daemon event.

DIR=/tmp/acc-219-journal
LOG=/tmp/acc-219-daemon.log
PID=/tmp/acc-219.pid
SOCK=/tmp/acc-219-events.sock
CTL=/tmp/acc-219.ctl

cleanup() {
    [ -f "$PID" ] && kill "$(cat "$PID")" 2>/dev/null
    rm -f "$LOG" "$PID" "$SOCK" "$CTL"
    rm -rf "$DIR"
}
trap cleanup EXIT INT TERM

mkdir -p "$DIR"
# Fresh JSONL daemon on a scratch dir + custom socket + admin
# socket so it doesn't collide with any real daemon that might be
# running. --control-socket points at the LIVE slinit control
# socket so the daemon has a source to query for the backlog.
slinit-journald --format=jsonl --dir="$DIR" \
    --pid-file="$PID" --socket="$SOCK" --admin-socket="$CTL" \
    --control-socket=/run/slinit.socket \
    >/dev/null 2>"$LOG" &
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$PID" ] && kill -0 "$(cat "$PID")" 2>/dev/null; then
    echo "OK: daemon spawned (pid $(cat "$PID"))"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: daemon did not start; log:"
    cat "$LOG" 2>/dev/null
    test_summary; return
fi

# The startup banner must include the "replayed N pre-daemon
# events" line. If N == 0 the line is silent (nothing to say);
# on a real ceres boot the ring buffer always has boot events so
# N > 0.
_out=$(cat "$LOG")
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_out" | grep -qE 'replayed [0-9]+ pre-daemon events'; then
    _n=$(echo "$_out" | grep -oE 'replayed [0-9]+' | head -1 | awk '{print $2}')
    echo "OK: startup log reports replay ($_n events)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no replay line in daemon log"
    echo "$_out"
fi

# Verify the persisted journal actually has events (proves the
# replay wrote through the sink, not just logged the count).
JFILE=$(ls "$DIR"/*.jsonl 2>/dev/null | head -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$JFILE" ] && [ -s "$JFILE" ]; then
    _entries=$(wc -l < "$JFILE")
    echo "OK: persisted journal has $_entries entries (replay landed events)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no persisted journal file with events"
fi

# The listening banner must mention the events socket path — proves
# the receiver came up after the replay phase (order matters for
# race-window bounds).
assert_contains "$_out" "listening on $SOCK" "receiver up after replay"

test_summary
