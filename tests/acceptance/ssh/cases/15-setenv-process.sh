#!/bin/sh
# 15-setenv-process — `slinitctl setenv SVC KEY=VAL` plus a restart must
# get KEY=VAL into the new process's environment (read back via
# /proc/PID/environ). Verifies both the protocol path and that env
# mutations propagate across exec.

SVC="acceptance-test-setenv"
KEY="ACCEPTANCE_TEST_KEY"
VAL="hello-from-acceptance"

trap 'svc_remove "$SVC"' EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true

# Confirm KEY is NOT yet present.
_pid="$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && tr '\0' '\n' < "/proc/$_pid/environ" 2>/dev/null \
    | grep -q "^${KEY}="; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $KEY unexpectedly present before setenv"
else
    echo "OK: $KEY absent before setenv"
fi

# Set the env var via the daemon, then restart so the new fork sees it.
slinitctl --system setenv "$SVC" "${KEY}=${VAL}" >/dev/null 2>&1

# getallenv should now list it (without needing a restart).
_all="$(slinitctl --system getallenv "$SVC" 2>&1)"
assert_contains "$_all" "${KEY}=${VAL}" "getallenv lists $KEY=$VAL"

slinitctl --system restart "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
_pid2="$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid2" ] && tr '\0' '\n' < "/proc/$_pid2/environ" 2>/dev/null \
    | grep -q "^${KEY}=${VAL}\$"; then
    echo "OK: $KEY=$VAL visible in pid $_pid2's environ"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $KEY=$VAL not in pid $_pid2 environ"
fi

# reset-env should drop it; subsequent restart should not show the var.
slinitctl --system reset-env "$SVC" >/dev/null 2>&1
slinitctl --system restart "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
_pid3="$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid3" ] && tr '\0' '\n' < "/proc/$_pid3/environ" 2>/dev/null \
    | grep -q "^${KEY}="; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $KEY still present after reset-env (pid $_pid3)"
else
    echo "OK: reset-env cleared $KEY"
fi

test_summary
