#!/bin/sh
# Test: a stop in a restart delay also stops a hard dependent waiting for
# the restart, and leaves nothing for the shutdown to wait on.
#
# A depends-on dependent started while its dependency waits out a
# restart delay is STARTING, waiting for the restart. Since 7cbe970 a
# stop there calls the restart off; until 57b0afb nothing told the
# dependent, so it stayed STARTING for good and the dependency stayed
# counted active (up to v4.0.3 the stop did nothing at all: the restart
# came back, which the run count catches). 303 covers the stop alone;
# this is the stop with a dependent waiting. With 57b0afb reverted, this
# case fails on "dept STARTING". Its shutdown still finished in about a
# second there, so the timed shutdown at the end is only a guard, run
# against a nested `slinit --user` so it can be timed.

ROOT=/tmp/316
S="$ROOT/svcs"
mkdir -p "$S"
printf '%s\n' 'type = internal' > "$S/boot"
printf '%s\n' 'type = process' \
    "command = /bin/sh -c \"echo run >> $ROOT/runs; sleep 0.5; exit 1\"" \
    'restart = on-failure' 'restart-delay = 4' 'restart-limit-count = 100' > "$S/flaky"
printf '%s\n' 'type = process' 'command = /bin/sleep 3160' 'restart = false' \
    'depends-on: flaky' > "$S/dept"

setsid /sbin/slinit --user --socket-path "$ROOT/sock" --services-dir "$S" \
    --log-file "$ROOT/log" </dev/null >/dev/null 2>&1 &
NEST_PID=$!
sl() { timeout 10 slinitctl --socket-path "$ROOT/sock" "$@"; }
st() { sl status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }
_e=0
until [ "$(st boot)" = STARTED ] || [ "$_e" -ge 20 ]; do sleep 0.5; _e=$((_e + 1)); done
[ "$(st boot)" = STARTED ] || skip_case "nested slinit did not come up"

sl --no-wait start flaky >/dev/null 2>&1
# Wait for the first run to end: flaky STOPPED inside its 4 s delay.
_e=0
until { [ "$(wc -l < "$ROOT/runs" 2>/dev/null || echo 0)" = 1 ] &&
        [ "$(st flaky)" = STOPPED ]; } || [ "$_e" -ge 40 ]; do
    sleep 0.2; _e=$((_e + 1))
done
[ "$(st flaky)" = STOPPED ] || skip_case "flaky never reached its restart delay"

sl --no-wait start dept >/dev/null 2>&1
sleep 0.5
_d=$(st dept)
echo "dept during flaky's delay: $_d"
[ "$_d" = STARTING ] || skip_case "dept is '$_d', not STARTING: precondition not met"

_out=$(sl stop flaky 2>&1); echo "stop flaky: $_out"
sleep 6   # past the delay: a restart would have come back by now
echo "runs: $(wc -l < "$ROOT/runs"), flaky $(st flaky), dept $(st dept)"
assert_eq "$(wc -l < "$ROOT/runs")" "1" "no run after the stop"
assert_eq "$(st dept)" "STOPPED" "the dependent waiting for the called-off restart is stopped"
assert_eq "$(st flaky)" "STOPPED" "the dependency stays stopped"

_t0=$(date +%s)
sl poweroff >/dev/null 2>&1
_e=0
while [ "$_e" -lt 30 ] && kill -0 "$NEST_PID" 2>/dev/null; do sleep 0.5; _e=$((_e + 1)); done
_alive=0; kill -0 "$NEST_PID" 2>/dev/null && _alive=1
echo "shutdown: $(( $(date +%s) - _t0 ))s, still running: $_alive"
kill -9 "$NEST_PID" 2>/dev/null
assert_eq "$_alive" "0" "the shutdown finishes within 15 s"

test_summary
