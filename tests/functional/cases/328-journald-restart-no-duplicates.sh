#!/bin/sh
# Test: restarting slinit-journald does not write PID 1's backlog twice.
#
# At every start slinit-journald replays PID 1's event ring so that
# events from before it listened are persisted. It replayed all of it:
# a clean restart doubled the journal (v4.0.3: 442 entries became 884,
# the same events with new sequence numbers), and a daemon restarted
# after a crash added up to 4096 duplicates each time. The replay now
# skips this boot's events strictly older than the last one an earlier
# run made durable (the persisted marker, written after every fsync and
# on a clean exit). After SIGKILL only the events since the last fsync
# may come again.

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

kill "$(cat "$D/pid")" 2>/dev/null
test_summary
