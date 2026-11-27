#!/bin/sh
# Test: rm-dep wakes a dependency that waits for its dependent to stop.
#
# A dependency that is stopping waits for its hard dependents to stop
# first. `slinitctl restart d` on a slow-stopping d releases leaf, which
# starts stopping and waits for d. Up to v4.0.3, `rm-dep d depends-on
# leaf` then removed the edge without waking leaf, which stayed STOPPING
# for good, and a shutdown waited for it until its timeout (4fddb11;
# 252 covers rm-dep on the start side only). Run against a nested
# `slinit --user`, so the shutdown can be timed.

R=/tmp/320
S="$R/svcs"
rm -rf "$R"; mkdir -p "$S"
printf '%s\n' 'type = internal' > "$S/boot"
printf '%s\n' 'type = scripted' 'command = /bin/true' 'stop-command = /bin/sleep 3' \
    'restart = false' 'depends-on: leaf' > "$S/d"
printf '%s\n' 'type = internal' > "$S/leaf"

setsid /sbin/slinit --user --socket-path "$R/sock" --services-dir "$S" \
    --log-file "$R/log" </dev/null >/dev/null 2>&1 &
NEST=$!
sl() { timeout 10 slinitctl --socket-path "$R/sock" "$@"; }
st() { sl status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }
_e=0
until [ "$(st boot)" = STARTED ] || [ "$_e" -ge 20 ]; do sleep 0.5; _e=$((_e + 1)); done
[ "$(st boot)" = STARTED ] || skip_case "nested slinit did not come up"

sl start d >/dev/null 2>&1
[ "$(st d)" = STARTED ] && [ "$(st leaf)" = STARTED ] || skip_case "d/leaf did not start"
sl --no-wait restart d >/dev/null 2>&1
sleep 0.5
echo "during the restart's stop: d $(st d), leaf $(st leaf)"
[ "$(st leaf)" = STOPPING ] || skip_case "leaf is '$(st leaf)', not STOPPING: precondition not met"

_out=$(sl rm-dep d depends-on leaf 2>&1); echo "rm-dep: $_out"
sleep 6
echo "after: d $(st d), leaf $(st leaf)"
assert_eq "$(st leaf)" "STOPPED" "leaf, no longer anybody's dependency, finishes stopping"

_t0=$(date +%s)
sl poweroff >/dev/null 2>&1
_e=0
while [ "$_e" -lt 30 ] && kill -0 "$NEST" 2>/dev/null; do sleep 0.5; _e=$((_e + 1)); done
_alive=0; kill -0 "$NEST" 2>/dev/null && _alive=1
echo "shutdown $(( $(date +%s) - _t0 ))s, still running: $_alive"
grep -i 'waiting for' "$R/log" 2>/dev/null | tail -1
kill -9 "$NEST" 2>/dev/null
assert_eq "$_alive" "0" "the shutdown finishes within 15 s"

test_summary
