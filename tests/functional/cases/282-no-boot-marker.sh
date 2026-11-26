#!/bin/sh
# Test: no-boot-marker = yes suppresses the "[ OK ] name" boot-console
# line, and only for that service.
#
# The boot console is on only in system mode without --log-file, so a
# nested `slinit -m` runs with its stdout in a file standing in for the
# console. A second, unmarked service in the same boot is the control:
# its line must be there, or the file proves nothing.

ROOT=/tmp/282
mkdir -p "$ROOT/svcs"
printf '%s\n' 'type = internal' 'depends-on: bm-plain' 'depends-on: bm-quiet' \
    > "$ROOT/svcs/boot"
printf '%s\n' 'type = internal' > "$ROOT/svcs/bm-plain"
printf '%s\n' 'type = internal' 'no-boot-marker = yes' > "$ROOT/svcs/bm-quiet"

setsid /sbin/slinit -m \
    --socket-path "$ROOT/sock" \
    --services-dir "$ROOT/svcs" \
    </dev/null >"$ROOT/console" 2>&1 &
NEST_PID=$!

_e=0
while [ "$_e" -lt 20 ] && ! grep -q 'bm-plain' "$ROOT/console" 2>/dev/null; do
    sleep 0.5; _e=$((_e + 1))
done
sleep 1
echo "--- nested console:"; cat "$ROOT/console"; echo "---"

assert_eq "$(grep -c '\[ OK \] bm-plain$' "$ROOT/console")" "1" \
    "an unmarked service prints its [ OK ] line"
assert_eq "$(grep -c 'bm-quiet' "$ROOT/console")" "0" \
    "no-boot-marker = yes prints no line for the service"
assert_eq "$(timeout 5 slinitctl --socket-path "$ROOT/sock" status bm-quiet 2>/dev/null |
    awk '/State:/ {print $2}')" "STARTED" "the quiet service did start"

kill -9 "$NEST_PID" 2>/dev/null
test_summary
