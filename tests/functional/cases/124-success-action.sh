#!/bin/sh
# Test: success-action fires when a service stops cleanly.
#
# reboot/poweroff/halt would take the VM down, so the case runs two
# nested `slinit --user` daemons, each with one process service that
# exits 0 after a second. In the first it has success-action = exit, so
# that daemon must terminate itself; in the second success-action =
# none, and the daemon must still be running. (The case used to check
# only that success-action = none parsed.)

ROOT=/tmp/functional-sxa
nest() {    # nest NAME ACTION -> starts a nested daemon, prints its pid
    mkdir -p "$ROOT/$1"
    cat > "$ROOT/$1/sxa-svc" <<EOF2
type = process
command = /bin/sh -c "sleep 1; exit 0"
success-action = $2
restart = no
EOF2
    cat > "$ROOT/$1/boot" <<'EOF2'
type = internal
waits-for: sxa-svc
EOF2
    setsid /sbin/slinit --user \
        --socket-path "$ROOT/$1.sock" \
        --services-dir "$ROOT/$1" \
        --log-file "$ROOT/$1.log" \
        </dev/null >/dev/null 2>&1 &
    echo $!
}

PID_EXIT=$(nest exit exit)
PID_NONE=$(nest none none)

_e=0
while [ "$_e" -lt 20 ] && kill -0 "$PID_EXIT" 2>/dev/null; do
    sleep 0.5; _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 "$PID_EXIT" 2>/dev/null; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: daemon with success-action=exit still running after 10s"
    sed 's/^/    /' "$ROOT/exit.log" 2>/dev/null | tail -5
else
    echo "OK: success-action=exit terminated the daemon"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if kill -0 "$PID_NONE" 2>/dev/null; then
    echo "OK: daemon with success-action=none is still running"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: daemon with success-action=none exited too; the check cannot tell"
fi

kill "$PID_EXIT" "$PID_NONE" 2>/dev/null
test_summary
