#!/bin/sh
# 39-pre-stop-hook — `pre-stop-hook` runs synchronously *before* SIGTERM
# is delivered to the main process. The hook receives the main PID as
# its trailing arg (runit-style — pkg/service/process.go:1988-2005).
#
# /bin/sh -c quirk: trailing args start at $0, not $1.

SVC="acceptance-test-prestop"
MARK="/run/acceptance-test-prestop.mark"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK"
}
trap cleanup EXIT INT TERM

rm -f "$MARK"

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
pre-stop-hook = /bin/sh -c 'echo "hook_pid=\$\$0" > $MARK'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

# Grab the main PID from slinitctl status output for cross-check.
# The status output pads "PID:" with multiple spaces; awk on whitespace
# picks the last field cleanly regardless of width.
_pid_main=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $NF; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_pid_main" in
    *[!0-9]*|"")
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: couldn't read main PID from status: '$_pid_main'"
        ;;
    *)
        echo "OK: main PID is $_pid_main"
        ;;
esac

# Trigger stop. Hook runs first; then SIGTERM, then process exit.
slinitctl --system stop "$SVC" >/dev/null 2>&1
_t=0
while [ "$_t" -lt 10 ]; do
    if [ "$(svc_state "$SVC")" = "STOPPED" ]; then
        break
    fi
    sleep 1
    _t=$((_t + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -r "$MARK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pre-stop-hook never wrote $MARK"
    test_summary
    exit 1
fi
echo "OK: pre-stop-hook ran"

_hookline=$(cat "$MARK")
case "$_hookline" in
    "hook_pid=$_pid_main")
        _TESTS_RUN=$((_TESTS_RUN + 1))
        echo "OK: pre-stop-hook received correct main PID ($_pid_main)"
        ;;
    *)
        _TESTS_RUN=$((_TESTS_RUN + 1))
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: pre-stop-hook arg mismatch: got '$_hookline', expected 'hook_pid=$_pid_main'"
        ;;
esac

test_summary
