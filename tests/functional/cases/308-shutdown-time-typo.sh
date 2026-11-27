#!/bin/sh
# Test: a mistyped shutdown time is a usage error, not an immediate shutdown.
#
# Up to v4.0.3 any word after the shutdown type that was not a valid time
# became the wall message of an IMMEDIATE shutdown: `slinitctl reboot
# 10:3O` (letter O) rebooted at once, and `poweroff +5min` powered off on
# the spot. Since 84b665f it exits 2 and nothing is sent, as SysV
# shutdown does.
#
# Aimed at a nested `slinit --user`, so the old behaviour shuts down the
# nested instance (observable) rather than the VM running the case.

ROOT=/tmp/308
mkdir -p "$ROOT/svcs"
printf '%s\n' 'type = internal' > "$ROOT/svcs/boot"

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

for _args in "reboot 10:3O" "poweroff +5min" "halt now-ish"; do
    _out=$(sl $_args 2>&1)
    _rc=$?
    echo "slinitctl $_args: rc=$_rc: $_out"
    assert_eq "$_rc" "2" "'$_args' is a usage error (exit 2)"
    sleep 1
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if kill -0 "$NEST_PID" 2>/dev/null &&
       [ "$(sl status boot 2>/dev/null | awk '/State:/ {print $2}')" = STARTED ]; then
        echo "OK: '$_args' did not shut anything down"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: '$_args' shut the instance down"
        break
    fi
done

# A valid time still schedules (and can be cancelled).
sl shutdown poweroff +10 >/dev/null 2>&1
assert_contains "$(sl shutdown --status 2>&1)" "scheduled" "a valid time still schedules"
sl shutdown -c >/dev/null 2>&1

kill -9 "$NEST_PID" 2>/dev/null
test_summary
