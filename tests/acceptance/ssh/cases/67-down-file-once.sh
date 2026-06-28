#!/bin/sh
# 67-down-file-once — runit-style `.down` marker + slinitctl once command.
#
# Two adjacent features intentionally bundled because both are "start the
# service but skip the auto-supervision sugar":
#
#  * .down marker (pkg/config/loader.go:725): a file <svc>.down next to
#    the service description tells the loader to mark the record so the
#    initial activation (boot / chain / waits-for) becomes a no-op. The
#    operator can still `slinitctl start` it explicitly; doing so clears
#    the in-memory flag (the on-disk marker stays — that's by design,
#    runit semantics).
#
#  * once command (cmd/slinitctl/main.go:281 → CmdOnceService): start the
#    service exactly once, no restart even if `restart = true` is set in
#    the config. Useful for diagnostics and one-shot maintenance reloads.

SVC_DOWN="acceptance-test-down"
SVC_ONCE="acceptance-test-once"
SVCFILE_DOWN="/etc/slinit.d/$SVC_DOWN"
SVCFILE_ONCE="/etc/slinit.d/$SVC_ONCE"
DOWNMARK="/etc/slinit.d/$SVC_DOWN.down"
MARKDIR="/tmp/acceptance-67"

cleanup() {
    for s in "$SVC_DOWN" "$SVC_ONCE"; do
        slinitctl --system stop "$s" 2>/dev/null
        slinitctl --system unload "$s" 2>/dev/null
        rm -f "/etc/slinit.d/$s" "/etc/slinit.d/$s.down"
    done
    rm -rf "$MARKDIR"
}
trap cleanup EXIT INT TERM
cleanup
mkdir -p "$MARKDIR"

# --- Down-file: a service marked down should not pull up automatically ----
cat > "$SVCFILE_DOWN" <<EOF
type = process
command = /bin/sh -c 'touch $MARKDIR/down-started; exec sleep 600'
restart = false
EOF
: > "$DOWNMARK"

# Load (but don't start) the service. After load, status should be STOPPED
# because the .down marker suppressed auto-start.
# `slinitctl status` triggers a load if needed.
_st=$(slinitctl --system status "$SVC_DOWN" 2>/dev/null | awk '/State:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STOPPED" ]; then
    echo "OK: $SVC_DOWN loaded but parked STOPPED (.down marker honored)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $SVC_DOWN unexpectedly in '$_st' despite .down marker"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "$MARKDIR/down-started" ]; then
    echo "OK: command never ran (down-started marker absent)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: command ran despite .down marker"
fi

# Explicit start clears the in-memory down flag and brings the service up.
slinitctl --system start "$SVC_DOWN" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 6 ]; do
    _st=$(slinitctl --system status "$SVC_DOWN" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: explicit start overrides the .down marker"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: explicit start didn't bring $SVC_DOWN up (state=$_st)"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARKDIR/down-started" ]; then
    echo "OK: command ran after explicit start"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: command still didn't run despite STARTED state"
fi

# --- Once command: restart=true is overridden — no auto-restart on exit ---
cat > "$SVCFILE_ONCE" <<EOF
type = process
command = /bin/sh -c 'echo "\$\$\$\$" > $MARKDIR/once-pid; exec sleep 2'
restart = true
EOF

slinitctl --system once "$SVC_ONCE" >/dev/null 2>&1
_e=0
while [ "$_e" -lt 6 ]; do
    _st=$(slinitctl --system status "$SVC_ONCE" 2>/dev/null | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: 'once' brought $SVC_ONCE to STARTED"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: 'once' didn't reach STARTED (state=$_st)"
fi

# Wait for the process to exit (sleep 2 in the command), then verify
# slinit honored "once" and did NOT restart it.
_pid1=$(cat "$MARKDIR/once-pid" 2>/dev/null)
sleep 4
_pid2=$(cat "$MARKDIR/once-pid" 2>/dev/null)
_st2=$(slinitctl --system status "$SVC_ONCE" 2>/dev/null | awk '/State:/ {print $2; exit}')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_pid1" = "$_pid2" ]; then
    echo "OK: pid file unchanged ($_pid1) — no restart cycle"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid rotated ($_pid1 -> $_pid2) — service restarted despite 'once'"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st2" in
    STOPPED|FAILED)
        echo "OK: post-exit state is '$_st2' (no auto-restart)"
        ;;
    STARTED|STARTING)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: post-exit state is '$_st2' — auto-restart kicked in despite 'once'"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: unexpected post-exit state '$_st2'"
        ;;
esac

test_summary
