#!/bin/sh
# Test: options = stops-last keeps a service, and what it hard-depends on,
# running until every other service has stopped at shutdown.
#
# Each service writes its name to one file when SIGTERM reaches it. The
# ordinary service takes two seconds to stop; the stops-last one and its
# dependency stop at once. Without the option their names come first;
# with it they must come after, the dependency last.

ROOT=/tmp/283
ORDER="$ROOT/order"
mkdir -p "$ROOT/svcs"
: > "$ORDER"

mkscript() { # name delay
    printf '%s\n' '#!/bin/sh' \
        "trap 'sleep $2; echo $1 >> $ORDER; exit 0' TERM" \
        'sleep 1000 & wait $!' 'sleep 1000 & wait $!' > "$ROOT/$1.sh"
    chmod +x "$ROOT/$1.sh"
}
mkscript slow 2
mkscript last 0
mkscript lastdep 0

printf '%s\n' 'type = internal' 'waits-for: sl-slow' 'waits-for: sl-last' > "$ROOT/svcs/boot"
printf '%s\n' 'type = process' "command = $ROOT/slow.sh" > "$ROOT/svcs/sl-slow"
printf '%s\n' 'type = process' "command = $ROOT/last.sh" 'options = stops-last' \
    'depends-on: sl-lastdep' > "$ROOT/svcs/sl-last"
printf '%s\n' 'type = process' "command = $ROOT/lastdep.sh" > "$ROOT/svcs/sl-lastdep"

setsid /sbin/slinit --user \
    --socket-path "$ROOT/sock" \
    --services-dir "$ROOT/svcs" \
    --log-file "$ROOT/log" \
    </dev/null >/dev/null 2>&1 &
NEST_PID=$!

sl() { timeout 5 slinitctl --socket-path "$ROOT/sock" "$@"; }
_e=0
until [ "$(sl status sl-last 2>/dev/null | awk '/State:/ {print $2}')" = STARTED ] ||
      [ "$_e" -ge 20 ]; do
    sleep 0.5; _e=$((_e + 1))
done
assert_eq "$(sl status sl-slow 2>/dev/null | awk '/State:/ {print $2}')" "STARTED" \
    "the ordinary service is up"
assert_eq "$(sl status sl-last 2>/dev/null | awk '/State:/ {print $2}')" "STARTED" \
    "the stops-last service is up"

sl poweroff >/dev/null 2>&1
_e=0
while [ "$_e" -lt 30 ] && kill -0 "$NEST_PID" 2>/dev/null; do
    sleep 0.5; _e=$((_e + 1))
done
kill -9 "$NEST_PID" 2>/dev/null
echo "stop order: $(tr '\n' ' ' < "$ORDER")"

assert_eq "$(tr '\n' ' ' < "$ORDER")" "slow last lastdep " \
    "stops-last stops after the others, its hard dependency after it"

test_summary
