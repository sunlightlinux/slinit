#!/bin/sh
# Test: a start pin does not outlive the edge or the dependent that
# carried it.
#
# A start pin travels down hard edges. Two ways it used to be left
# behind for good, so the dependency could be stopped neither by a stop,
# nor a forced stop, nor the shutdown (which then waited it out):
#  - rm-dep of the edge that carried it (09c65b4);
#  - a depends-ms dependency pinned while it was still starting, whose
#    edge then went soft, so unpin never walked it (ff8c51e).
# Run against a nested `slinit --user`, so the final shutdown can be
# timed.

ROOT=/tmp/313
mkdir -p "$ROOT/svcs"
S="$ROOT/svcs"
printf '%s\n' 'type = internal' > "$S/boot"
for n in top mid leaf top2; do
    printf '%s\n' 'type = process' "command = /bin/sleep 313$n" 'restart = false' > "$S/$n"
done
echo 'depends-on: mid' >> "$S/top"
echo 'depends-on: leaf' >> "$S/mid"
printf '%s\n' 'type = scripted' 'command = /bin/sleep 2' 'stop-command = /bin/true' \
    'restart = false' > "$S/ms"
echo 'depends-ms: ms' >> "$S/top2"

setsid /sbin/slinit --user --socket-path "$ROOT/sock" --services-dir "$S" \
    --log-file "$ROOT/log" </dev/null >/dev/null 2>&1 &
NEST_PID=$!
sl() { timeout 10 slinitctl --socket-path "$ROOT/sock" "$@"; }
st() { sl status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }
waitst() { # svc state
    _e=0
    until [ "$(st "$1")" = "$2" ] || [ "$_e" -ge 20 ]; do sleep 0.5; _e=$((_e + 1)); done
}
waitst boot STARTED
[ "$(st boot)" = STARTED ] || skip_case "nested slinit did not come up"

# 1. rm-dep of the edge that carried the pin.
sl --pin start top >/dev/null 2>&1
waitst leaf STARTED
[ "$(st leaf)" = STARTED ] || skip_case "leaf did not start under the pinned top"
_out=$(sl rm-dep mid depends-on leaf 2>&1); echo "rm-dep: $_out"
sl unpin top >/dev/null 2>&1
_out=$(sl stop leaf 2>&1); echo "stop leaf: $_out"
waitst leaf STOPPED
assert_eq "$(st leaf)" "STOPPED" "after rm-dep of the edge that pinned it, leaf can be stopped"

# 2. A milestone pinned while it was still starting.
sl --no-wait --pin start top2 >/dev/null 2>&1
waitst top2 STARTED
[ "$(st ms)" = STARTED ] || skip_case "ms did not start (state $(st ms))"
sl unpin top2 >/dev/null 2>&1
sl stop top2 >/dev/null 2>&1
waitst top2 STOPPED
_out=$(sl --force stop ms 2>&1); echo "force stop ms: $_out"
waitst ms STOPPED
assert_eq "$(st ms)" "STOPPED" "after unpin, a milestone pinned while starting can be stopped"

# 3. Nothing pinned is left to hold the shutdown.
_t0=$(date +%s)
sl poweroff >/dev/null 2>&1
_e=0
while [ "$_e" -lt 30 ] && kill -0 "$NEST_PID" 2>/dev/null; do sleep 0.5; _e=$((_e + 1)); done
_alive=0; kill -0 "$NEST_PID" 2>/dev/null && _alive=1
echo "shutdown: $(( $(date +%s) - _t0 ))s, still running: $_alive"
kill -9 "$NEST_PID" 2>/dev/null
assert_eq "$_alive" "0" "the shutdown finishes within 15 s"

test_summary
