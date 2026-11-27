#!/bin/sh
# Test: slinit-journald does not append to a binary journal its previous
# run never closed.
#
# The binary writer keeps the day file's header marked online while it
# is open, so a slinit-journald killed with SIGKILL always leaves it so.
# Up to v4.0.3 the restarted daemon reopened that file and appended at
# its header's tail; a writer killed mid-append left an entry slot and
# hash links pointing at or past that tail, and the next append's objects
# then read back as a duplicate entry or broke the directory (bb2c622).
# Now a file still marked online is renamed aside, as systemd-journald
# does with a dirty file, and a new day file is started.

D=/tmp/325
rm -rf "$D"; mkdir -p "$D/j"
jd() {
    slinit-journald --format=binary --dir="$D/j" --pid-file="$D/pid" \
        --socket="$D/events.sock" --admin-socket="$D/ctl" >/dev/null 2>>"$D/err" &
}

jd
_e=0
until ls "$D"/j/*.journal >/dev/null 2>&1 || [ "$_e" -ge 20 ]; do sleep 0.25; _e=$((_e + 1)); done
ls "$D"/j/*.journal >/dev/null 2>&1 || skip_case "slinit-journald wrote no binary file: $(tail -2 "$D/err")"
sleep 1
kill -9 "$(cat "$D/pid")" 2>/dev/null
sleep 0.5
_before=$(ls "$D"/j/*.journal | wc -l)
echo "after SIGKILL: $(ls "$D"/j | tr '\n' ' ')"

jd
sleep 2
echo "after restart: $(ls "$D"/j | tr '\n' ' ')"
echo "stderr: $(tr '\n' ' ' < "$D/err")"
_after=$(ls "$D"/j/*.journal | wc -l)

_TESTS_RUN=$((_TESTS_RUN + 1))
if ls "$D"/j/*.*.journal >/dev/null 2>&1 && [ "$_after" -gt "$_before" ]; then
    echo "OK: the file left online was set aside and a new one started"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the restarted daemon reused the file its predecessor never closed"
fi
assert_contains "$(cat "$D/err")" "not closed (state online)); rotated to" "the daemon says why"
_out=$(slinit-journalctl -D "$D/j" --no-pager 2>&1); _rc=$?
assert_eq "$_rc" "0" "the directory, set-aside file included, still reads"

kill "$(cat "$D/pid")" 2>/dev/null
test_summary
