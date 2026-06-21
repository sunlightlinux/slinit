#!/bin/sh
# 38-finish-command — `finish-command` runs after the main process exits
# (runit-style). slinit appends two trailing args:
#   - exit code (or -1 on signal)
#   - wait status (signal number on signal, 0 otherwise)
# pkg/service/process.go:1930-1942.
#
# /bin/sh -c quirk: when invoked as `sh -c SCRIPT a b`, $0=a, $1=b
# (the first trailing arg goes to $0, not $1).

SVC="acceptance-test-finish"
MARK="/run/acceptance-test-finish.mark"

cleanup() {
    svc_remove "$SVC"
    rm -f "$MARK"
}
trap cleanup EXIT INT TERM

rm -f "$MARK"

# Main exits 7 → finish-command should run with args ("7", "0").
# Two layers of escape for $0/$1 — slinit's parser strips $$ → $ during
# expandEnvVarsForCommand (parser.go:2566), then the child sh expands.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'sleep 1; exit 7'
finish-command = /bin/sh -c 'echo "a0=\$\$0 a1=\$\$1" > $MARK'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
# Main exits ~1s later. finish-command runs after — give it some slack.
sleep 3

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -r "$MARK" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: finish-command never wrote $MARK"
    test_summary
    exit 1
fi
echo "OK: finish-command ran (marker present)"

_line=$(cat "$MARK")
assert_eq "$_line" "a0=7 a1=0" "finish-command args (exit=7, wait-status=0)"

test_summary
