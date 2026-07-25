#!/bin/sh
# 186-slinitctl-global-env-persistence — setenv-global writes into the
# daemon's global env dictionary, inherited by every subsequently
# started service. getallenv-global reads back; unsetenv-global drops.
# The round-trip proves each verb reaches the daemon and mutates the
# shared state.

KEY="ACC_TEST_GLOBAL_$$"
VAL="global-env-value-186"
SVC="acceptance-test-global-env"

cleanup() {
    slinitctl --system unsetenv-global "$KEY" >/dev/null 2>&1 || true
    svc_remove "$SVC"
    rm -f /tmp/acceptance-global-env.env
}
trap cleanup EXIT INT TERM

# --- setenv-global lands in getallenv-global ---------------------------
slinitctl --system setenv-global "$KEY=$VAL" >/dev/null
_env=$(slinitctl --system getallenv-global 2>&1)
assert_contains "$_env" "$KEY=$VAL" "setenv-global visible in getallenv-global"

# --- a fresh svc inherits the global env ------------------------------
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'env > /tmp/acceptance-global-env.env; exec sleep 600'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "^${KEY}=${VAL}$" /tmp/acceptance-global-env.env 2>/dev/null; then
    echo "OK: child inherited global env $KEY=$VAL"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $KEY not in child env"
fi

# --- unsetenv-global removes the key ---------------------------------
slinitctl --system unsetenv-global "$KEY" >/dev/null
_env=$(slinitctl --system getallenv-global 2>&1)
assert_not_contains "$_env" "$KEY=" "unsetenv-global removed $KEY from global env"

test_summary
