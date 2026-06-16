#!/bin/sh
# 16-setenv-global — setenv-global / unsetenv-global / getallenv-global.
# Variables set globally are inherited by services started afterwards.

KEY="ACCEPTANCE_GLOBAL_KEY"
VAL="globval-$$"
SVC="acceptance-test-globalenv-reader"

cleanup() {
    svc_remove "$SVC"
    slinitctl --system unsetenv-global "$KEY" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Make sure it's not lingering from a previous failed run.
slinitctl --system unsetenv-global "$KEY" 2>/dev/null || true

slinitctl --system setenv-global "${KEY}=${VAL}" >/dev/null 2>&1
_all="$(slinitctl --system getallenv-global 2>&1)"
assert_contains "$_all" "${KEY}=${VAL}" "getallenv-global shows the var"

# A service started after the setenv-global must inherit the var.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true

_pid="$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && tr '\0' '\n' < "/proc/$_pid/environ" 2>/dev/null \
    | grep -q "^${KEY}=${VAL}\$"; then
    echo "OK: $KEY inherited by $SVC (pid $_pid)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $KEY=$VAL not in $SVC's environ (pid $_pid)"
fi

# unsetenv-global drops it from the global env.
slinitctl --system unsetenv-global "$KEY" >/dev/null 2>&1
_after="$(slinitctl --system getallenv-global 2>&1)"
assert_not_contains "$_after" "${KEY}=" "unsetenv-global removed $KEY"

test_summary
