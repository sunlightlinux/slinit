#!/bin/sh
# Test: restarting slinit-journald does not write PID 1's backlog twice.
#
# At every start slinit-journald replays PID 1's event ring so that
# events from before it listened are persisted. It replayed all of it:
# a clean restart doubled the journal (v4.0.3: 442 entries became 884,
# the same events with new sequence numbers), and a daemon restarted
# after a crash added up to 4096 duplicates each time. The replay now
# skips the backlog events of this boot that an earlier
# run already wrote, judged by the journal on disk. Events that never
# reached the journal are replayed, so nothing in PID 1's ring is lost.

D=/tmp/328
rm -rf "$D"; mkdir -p "$D/j"
jd() {
    slinit-journald --format=binary --dir="$D/j" --pid-file="$D/pid" \
        --socket="$D/ev.sock" --admin-socket="$D/ctl" >/dev/null 2>>"$D/err" &
}
count() { slinit-journalctl -D "$D/j" --no-pager -o json 2>/dev/null | wc -l; }
dups() { # entries whose timestamp and message both occur more than once
    slinit-journalctl -D "$D/j" --no-pager -o json 2>/dev/null |
        sed 's/"_SEQNUM":"[0-9]*"//; s/"seq":[0-9]*//' | sort | uniq -d | wc -l
}

jd; sleep 2
n1=$(count)
[ "$n1" -gt 50 ] || skip_case "first run persisted only $n1 entries"
kill "$(cat "$D/pid")"; sleep 1          # clean exit: the marker is current
jd; sleep 2
n2=$(count); d2=$(dups)
echo "first run: $n1 entries; after a clean restart: $n2, duplicated: $d2"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$d2" -le 5 ] && [ "$n2" -lt $((n1 + n1 / 2)) ]; then
    echo "OK: a clean restart did not write the backlog again"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a clean restart wrote $((n2 - n1)) more entries, $d2 of them duplicates"
fi

slinitctl --system status boot >/dev/null 2>&1   # an event or two in between
sleep 1
kill -9 "$(cat "$D/pid")"; sleep 0.5
jd; sleep 2
n3=$(count); d3=$(dups)
echo "after SIGKILL and a restart: $n3 entries, duplicated: $d3"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$d3" -le 40 ]; then
    echo "OK: after a crash at most the events since the last fsync came again ($d3)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a restart after SIGKILL duplicated $d3 entries"
fi
_out=$(slinit-journalctl -D "$D/j" --no-pager 2>&1); _rc=$?
assert_eq "$_rc" "0" "the directory still reads"
kill "$(cat "$D/pid")" 2>/dev/null; sleep 1

# Nothing may be skipped that is not on disk. Here the daemon listens on
# PID 1's own events socket, so events also arrive live, and some are
# lost on the way (emitted after the backlog was read but before the
# socket was bound, or while PID 1 backs off after a refused connect).
# A replay filtered by a high-water mark would drop those for good once
# later events pushed the mark past them; filtered by what is on disk,
# the next start puts them back.
E=/tmp/328e
rm -rf "$E"; mkdir -p "$E/j"
jde() {
    slinit-journald --format=binary --dir="$E/j" --pid-file="$E/pid" \
        --admin-socket="$E/ctl" >/dev/null 2>>"$E/err" &
}
printf '%s\n' 'type = process' 'command = /bin/sleep 3280' 'restart = false' \
    > /etc/slinit.d/test-328
churn() { for _ in 1 2 3; do
    slinitctl --system start test-328 >/dev/null 2>&1
    slinitctl --system stop test-328 >/dev/null 2>&1
done; }
jde; sleep 2
kill -9 "$(cat "$E/pid")"; sleep 0.3
jde; churn                       # races the new daemon's start
sleep 3; churn                   # delivered live
kill "$(cat "$E/pid")"; sleep 1  # clean exit
jde; sleep 2                     # this start must repair any loss
kill "$(cat "$E/pid")"; sleep 1
# The journal keeps microseconds, the ring nanoseconds: compare to the µs.
key() { sed -n 's/.*"ts":\([0-9]*\)[0-9][0-9][0-9][,}].*"msg":"\([^"]*\)".*/\1 \2/p' | sort; }
# Without -D, slinit-journalctl reads PID 1's ring: this boot only.
slinit-journalctl --no-pager -o json 2>/dev/null | key > "$E/ring"
slinit-journalctl -D "$E/j" --no-pager -o json 2>/dev/null | key | sort -u > "$E/disk"
_missing=$(comm -23 "$E/ring" "$E/disk" | wc -l)
echo "PID 1's ring for this boot: $(wc -l < "$E/ring") events; missing from disk: $_missing"
comm -23 "$E/ring" "$E/disk" | head -3
[ "$(wc -l < "$E/ring")" -gt 50 ] || skip_case "could not read PID 1's ring"
assert_eq "$_missing" "0" "every event of this boot in PID 1's ring is in the journal"

test_summary
