#!/bin/sh
# 68-slinit-monitor — event-watcher CLI that subscribes to SERVICEEVENT
# push notifications and shells out to a command per event.
#
# slinit-monitor (cmd/slinit-monitor/main.go) connects to a slinit
# control socket, sends LISTENSERVICEEVENT, and translates each push
# into a single `sh -c COMMAND` invocation with %-substitutions:
#
#   %n  service name
#   %s  status text  (started / stopped / failed)
#   %v  variable value     (env mode, not used here)
#   %%  literal percent
#
# We park slinit-monitor in the background, drive a probe service
# through STARTED → STOPPED, and assert the monitor wrote the two
# expected lines to a sink file.

SVC="acceptance-test-mon-target"
SVCFILE="/etc/slinit.d/$SVC"
MONLOG="/tmp/acceptance-mon.log"
HOOK="/tmp/acceptance-mon-hook.sh"
MON_PID=""

cleanup() {
    if [ -n "$MON_PID" ]; then
        kill -TERM "$MON_PID" 2>/dev/null
        for _ in 1 2 3; do
            kill -0 "$MON_PID" 2>/dev/null || break
            sleep 1
        done
        kill -KILL "$MON_PID" 2>/dev/null
    fi
    slinitctl --system stop "$SVC" 2>/dev/null
    slinitctl --system unload "$SVC" 2>/dev/null
    rm -f "$SVCFILE" "$MONLOG" "$HOOK"
}
trap cleanup EXIT INT TERM
cleanup

# slinit-monitor runs `-c COMMAND` via exec.Command (no shell), and its
# splitCommand only honors double quotes — single quotes are left as
# literal characters that break sh -c. The simplest robust wiring is a
# tiny on-disk helper that takes name+status as positional args and
# does the redirect itself.
cat > "$HOOK" <<EOF
#!/bin/sh
echo "\$1 \$2" >> $MONLOG
EOF
chmod +x "$HOOK"

cat > "$SVCFILE" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

nohup slinit-monitor -s -c "$HOOK %n %s" "$SVC" \
    > /tmp/acceptance-mon.stderr 2>&1 &
MON_PID=$!

# Monitor takes a moment to dial the socket and register the listener;
# starting the service before that loses the first event. Poll for the
# log file's existence as a proxy.
sleep 1

slinitctl --system start "$SVC" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 6 ]; do
    _st=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: $SVC reached STARTED"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $SVC stuck at '$_st'"
    test_summary
    exit 1
fi

# Give the monitor a beat to write its line, then stop the service and
# wait again for the STOPPED event.
sleep 1
slinitctl --system stop "$SVC" >/dev/null 2>&1
sleep 2

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MONLOG" ]; then
    echo "OK: monitor wrote to $MONLOG"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $MONLOG never appeared. monitor stderr:"
    tail -20 /tmp/acceptance-mon.stderr 2>/dev/null | sed 's/^/  | /'
    test_summary
    exit 1
fi

# --- Probe: STARTED event reached the command -----------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE "^$SVC started$" "$MONLOG"; then
    echo "OK: STARTED event delivered with %n=$SVC %s=started"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no '$SVC started' line; saw:"
    sed 's/^/  | /' "$MONLOG"
fi

# --- Probe: STOPPED event reached the command -----------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE "^$SVC stopped$" "$MONLOG"; then
    echo "OK: STOPPED event delivered with %n=$SVC %s=stopped"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no '$SVC stopped' line; saw:"
    sed 's/^/  | /' "$MONLOG"
fi

# --- Probe: %% literal still gets through the substitution code -----
# Run a one-shot monitor with -e (exit-after-first) and -i (replay
# initial state), so we don't have to drive the service again. The
# only thing we care about here is the literal-percent escape.
ONE_LOG="/tmp/acceptance-mon-pct.log"
PCTHOOK="/tmp/acceptance-mon-pct-hook.sh"
rm -f "$ONE_LOG"
cat > "$PCTHOOK" <<EOF
#!/bin/sh
echo "\$1" >> $ONE_LOG
EOF
chmod +x "$PCTHOOK"
slinitctl --system start "$SVC" >/dev/null 2>&1
sleep 1
slinit-monitor -s -i -e -c "$PCTHOOK %n%%done" "$SVC" \
    >/dev/null 2>&1 &
ONE_PID=$!
# initial-state replay fires immediately; -e tells the monitor to exit
# after the first command exec. Wait up to 5s for the file.
_e=0
while [ "$_e" -lt 5 ]; do
    [ -e "$ONE_LOG" ] && break
    sleep 1
    _e=$((_e + 1))
done
kill "$ONE_PID" 2>/dev/null
wait "$ONE_PID" 2>/dev/null

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -qE "^$SVC%done$" "$ONE_LOG" 2>/dev/null; then
    echo "OK: %% escape lands as a single % literal"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: %% escape mis-handled; got: $(cat "$ONE_LOG" 2>/dev/null)"
fi
rm -f "$ONE_LOG" "$PCTHOOK"

test_summary
