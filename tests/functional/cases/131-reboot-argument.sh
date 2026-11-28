#!/bin/sh
# Test: reboot-argument is stored and handed to the system action.
#
# A real reboot is out of scope, so a nested `slinit --user` runs a
# service that exits 0 with success-action = reboot and
# reboot-argument = recovery. Outside PID 1 a reboot action ends the
# daemon without the reboot(2) call, but the handler still stores the
# argument for LINUX_REBOOT_CMD_RESTART2 and logs it, and the daemon
# must exit. (The case used to check only that the directive parsed,
# then the "not yet forwarded" line that went with exit; an argument
# is only for a reboot.)

ROOT=/tmp/functional-rebarg
mkdir -p "$ROOT/svcs"
cat > "$ROOT/svcs/rebarg-svc" <<'EOF2'
type = process
command = /bin/sh -c "sleep 1; exit 0"
success-action = reboot
reboot-argument = recovery
restart = no
EOF2
cat > "$ROOT/svcs/boot" <<'EOF2'
type = internal
waits-for: rebarg-svc
EOF2

setsid /sbin/slinit --user \
    --socket-path "$ROOT/sock" \
    --services-dir "$ROOT/svcs" \
    --log-file "$ROOT/log" \
    </dev/null >/dev/null 2>&1 &
NEST_PID=$!

_e=0
while [ "$_e" -lt 20 ] && kill -0 "$NEST_PID" 2>/dev/null; do
    sleep 0.5; _e=$((_e + 1))
done

assert_eq "$(grep -c 'system-action reboot with argument "recovery"' "$ROOT/log" 2>/dev/null)" "1" \
    "system action was handed reboot-argument=recovery"
_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 "$NEST_PID" 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: nested daemon did not run the success action"
    kill "$NEST_PID" 2>/dev/null
else
    echo "OK: nested daemon ran the success action"
fi

test_summary
