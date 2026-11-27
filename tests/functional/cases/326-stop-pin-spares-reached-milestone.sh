#!/bin/sh
# Test: a stop pin that refuses its service's restart fails the dependents
# waiting for it, but spares a milestone dependent already past its
# milestone.
#
# c83cad8 made a stop pin that refuses a stopping service's restart fail
# the dependents whose start waited on it (319 A covers that). Its first
# version failed every STARTING dependent with a depends-ms edge too,
# including one whose milestone was reached before the stop: that one
# was starting on its own, and slinit marked it STOPPED with its start
# script still running, untracked (5afa5d2, found by the seventh
# review). Measured: v4.0.3 fails this case on "e", left STARTING (the
# gap c83cad8 closed); this branch with 5afa5d2 reverted fails it on
# "m", STOPPED at 4 s and still at 8 s with its start script done. Run
# on a nested `slinit --user`.

R=/tmp/326
S="$R/svcs"
rm -rf "$R"; mkdir -p "$S"
printf '%s\n' 'type = internal' > "$S/boot"
printf '%s\n' 'type = scripted' 'command = /bin/true' 'stop-command = /bin/sleep 3' \
    'restart = false' > "$S/d"
printf '%s\n' 'type = scripted' "command = /bin/sh -c \"sleep 6; touch $R/m-done\"" \
    'stop-command = /bin/true' 'restart = false' 'depends-ms: d' > "$S/m"
printf '%s\n' 'type = process' 'command = /bin/sleep 3260' 'restart = false' \
    'depends-on: d' > "$S/e"

setsid /sbin/slinit --user --socket-path "$R/sock" --services-dir "$S" \
    --log-file "$R/log" </dev/null >/dev/null 2>&1 &
NEST=$!
sl() { timeout 10 slinitctl --socket-path "$R/sock" "$@"; }
st() { sl status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }
_e=0
until [ "$(st boot)" = STARTED ] || [ "$_e" -ge 20 ]; do sleep 0.5; _e=$((_e + 1)); done
[ "$(st boot)" = STARTED ] || skip_case "nested slinit did not come up"

sl start d >/dev/null 2>&1
sl --no-wait start m >/dev/null 2>&1
sleep 0.3
[ "$(st m)" = STARTING ] || skip_case "m is '$(st m)', not STARTING past its milestone"
sl --no-wait stop d >/dev/null 2>&1
sl --no-wait --pin stop d >/dev/null 2>&1
sleep 0.3
[ "$(st d)" = STOPPING ] || skip_case "d is '$(st d)', not STOPPING"
sl --no-wait start e >/dev/null 2>&1

# d finishes stopping at about 3 s, m's start runs until about 6 s.
sleep 4
echo "at 4 s: d $(st d), m $(st m), e $(st e)"
assert_eq "$(st m)" "STARTING" "m, past its milestone, is still starting when d's restart is refused"
_e_state=$(st e)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_e_state" != STARTING ] && [ "$_e_state" != STARTED ]; then
    echo "OK: e, whose start waited on d, is failed ($_e_state)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: e is $_e_state after d's stop pin refused to start d"
fi
sleep 4
echo "at 8 s: m $(st m), start script finished: $([ -e "$R/m-done" ] && echo yes || echo no)"
assert_eq "$(st m)" "STARTED" "m is STARTED once its own start finished"

kill -9 "$NEST" 2>/dev/null
test_summary
