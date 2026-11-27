#!/bin/sh
# Test: a shutdown is not held up by a service pinned while STARTING.
#
# `slinitctl --pin start` of a triggered service that never gets its
# trigger leaves it STARTING with a start pin. A shutdown stops every
# service and drops every pin; the stop asked of a pinned service is put
# off until the pin goes. Up to 5f02f2b, unpin carried that held stop out
# only for a STARTED service, so this one kept "starting" and the
# shutdown waited out its emergency timeout instead of finishing.
#
# Run against a nested `slinit --user`, so the shutdown can be watched
# (and timed) from the outside. Its emergency timeout is far above the
# bound used here, so finishing within the bound means the held stop was
# carried out, not timed out.

ROOT=/tmp/304
mkdir -p "$ROOT/svcs"
printf '%s\n' 'type = internal' > "$ROOT/svcs/boot"
printf '%s\n' 'type = triggered' > "$ROOT/svcs/needtrig"

setsid /sbin/slinit --user \
    --socket-path "$ROOT/sock" \
    --services-dir "$ROOT/svcs" \
    --log-file "$ROOT/log" \
    </dev/null >/dev/null 2>&1 &
NEST_PID=$!

sl() { timeout 5 slinitctl --socket-path "$ROOT/sock" "$@"; }
_e=0
until [ "$(sl status boot 2>/dev/null | awk '/State:/ {print $2}')" = STARTED ] ||
      [ "$_e" -ge 20 ]; do
    sleep 0.5; _e=$((_e + 1))
done
[ "$_e" -lt 20 ] || skip_case "nested slinit did not come up"

# --no-wait is global: it goes before the verb.
sl --no-wait --pin start needtrig >/dev/null 2>&1
sleep 1
_st=$(sl status needtrig 2>/dev/null | awk '/State:/ {print $2}')
[ "$_st" = STARTING ] || skip_case "needtrig is '$_st', not STARTING: precondition not met"

_t0=$(date +%s)
sl poweroff >/dev/null 2>&1
_e=0
while [ "$_e" -lt 30 ] && kill -0 "$NEST_PID" 2>/dev/null; do
    sleep 0.5; _e=$((_e + 1))
done
_alive=0
kill -0 "$NEST_PID" 2>/dev/null && _alive=1
_secs=$(( $(date +%s) - _t0 ))
kill -9 "$NEST_PID" 2>/dev/null
echo "shutdown took ${_secs}s, still running after the bound: $_alive"
grep -i 'needtrig' "$ROOT/log" 2>/dev/null | tail -5

assert_eq "$_alive" "0" "shutdown finishes within 15 s with a service pinned while STARTING"

test_summary
