#!/bin/sh
# 56-container-mode — spawn a nested slinit with `-o` (container mode)
# alongside the live PID 1, talk to it via its own socket, deploy and
# start a service in its isolated registry, then tear it down.
#
# InitContainer (pkg/shutdown/init_pid1.go:406) is non-destructive: it
# only sets the subreaper flag for the child slinit, ignores terminal
# signals, and runs the clock-guard. No mounts, no /dev/console, no CAD
# handler — so this is safe to run on a live VM.

SUBDIR="/tmp/acceptance-test-cnt"
SVCDIR="$SUBDIR/svc.d"
SOCKET="$SUBDIR/slinit.sock"
LOG="$SUBDIR/slinit.log"
TEST_SVC="acceptance-test-cnt-leaf"
MARKER="$SUBDIR/leaf.mark"
SLINIT_PID=""

cleanup() {
    if [ -n "$SLINIT_PID" ]; then
        kill -TERM "$SLINIT_PID" 2>/dev/null
        # Give the child up to 3s to exit cleanly before SIGKILL.
        for _ in 1 2 3; do
            kill -0 "$SLINIT_PID" 2>/dev/null || break
            sleep 1
        done
        kill -KILL "$SLINIT_PID" 2>/dev/null
    fi
    rm -rf "$SUBDIR"
}
trap cleanup EXIT INT TERM

rm -rf "$SUBDIR"
mkdir -p "$SVCDIR"

# Boot service: depends on the leaf so the leaf comes up automatically.
cat > "$SVCDIR/boot" <<EOF
type = internal
depends-on: $TEST_SVC
EOF

cat > "$SVCDIR/$TEST_SVC" <<EOF
type = process
command = /bin/sh -c 'touch $MARKER; while :; do sleep 60; done'
restart = false
EOF

# Spawn the nested slinit. -o = container mode, -m = system manager
# (needed so it accepts the boot service name in argv), -p = our
# isolated socket, -d = our isolated service-description dir.
nohup slinit -o -m -p "$SOCKET" -d "$SVCDIR" -t boot >"$LOG" 2>&1 &
SLINIT_PID=$!

# Wait for the socket to appear; bail after 5s.
_e=0
while [ "$_e" -lt 5 ]; do
    [ -S "$SOCKET" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -S "$SOCKET" ]; then
    echo "OK: nested slinit (pid $SLINIT_PID) opened $SOCKET"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: socket $SOCKET never appeared. tail of log:"
    tail -20 "$LOG" 2>/dev/null | sed 's/^/  | /'
    test_summary
    exit 1
fi

# --- Probe 1: list shows the leaf, NOT a system-wide service -------
# (the leaf belongs to the nested slinit; PID 1's `boot` should NOT
# appear in the nested registry — confirms socket isolation.)
_list=$(slinitctl --socket-path "$SOCKET" list 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_list" in
    *"$TEST_SVC"*)
        echo "OK: nested list contains $TEST_SVC"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: nested list missing $TEST_SVC:"
        echo "$_list" | sed 's/^/  | /'
        ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_list" in
    *socklog*|*sshd*|*dbus*)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: nested list leaked a system service (sockets crossed?):"
        echo "$_list" | grep -E "socklog|sshd|dbus" | sed 's/^/  | /'
        ;;
    *)
        echo "OK: nested registry is isolated from PID 1 (no socklog/sshd/dbus)"
        ;;
esac

# --- Probe 2: status reports STARTED + marker present --------------
# Wait briefly for boot graph to settle.
_e=0
while [ "$_e" -lt 8 ]; do
    _st=$(slinitctl --socket-path "$SOCKET" status "$TEST_SVC" 2>/dev/null \
        | awk '/State:/ {print $2; exit}')
    [ "$_st" = "STARTED" ] && break
    sleep 1
    _e=$((_e + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_st" = "STARTED" ]; then
    echo "OK: $TEST_SVC reached STARTED inside nested slinit"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $TEST_SVC stuck at '$_st' inside nested slinit"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$MARKER" ]; then
    echo "OK: marker present — leaf command actually ran under nested PID-1"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: marker $MARKER missing despite STARTED state"
fi

# --- Probe 3: parent PID 1 socket still works (isolation check) ----
_psl=$(slinitctl --system list 2>&1 | grep -c socklog || true)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_psl" -ge 1 ]; then
    echo "OK: parent PID 1 socket still responsive (socklog visible)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: parent PID 1 socket misbehaving (no socklog in list)"
fi

test_summary
