#!/bin/sh
# 195-slinitctl-per-svc-env-round-trip — per-service (not global)
# setenv / getallenv / unsetenv round-trip. Global env is covered by
# 16-setenv-global and 186-slinitctl-global-env-persistence; this
# case verifies the analogous per-service verbs and the child
# actually inherits the runtime-set value.

SVC="acceptance-test-persvcenv"
KEY="ACC_TEST_PERSVC_$$"
VAL="per-svc-value-195"
ENV_FILE=/tmp/acceptance-persvc-env.dump

cleanup() { svc_remove "$SVC"; rm -f "$ENV_FILE"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
# Runtime env refs need \$\$VAR — slinit's parser pre-expands \$VAR
# at load time (would collapse to empty). \$\$KEY defers to the
# runtime shell where the setenv-injected value is visible.
command = /bin/sh -c "env > $ENV_FILE; exec sleep 600"
restart = false
EOF

# Set per-svc env BEFORE starting so the injected value is available
# when the child's env is captured.
slinitctl --system setenv "$SVC" "$KEY=$VAL" >/dev/null
_env=$(slinitctl --system getallenv "$SVC" 2>&1)
assert_contains "$_env" "$KEY=$VAL" "setenv value visible in getallenv"

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10
sleep 1

_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "^${KEY}=${VAL}$" "$ENV_FILE" 2>/dev/null; then
    echo "OK: child inherited per-svc setenv $KEY=$VAL"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $KEY not in child env"
fi

# unsetenv drops the key from the per-svc dict.
slinitctl --system unsetenv "$SVC" "$KEY" >/dev/null
_env=$(slinitctl --system getallenv "$SVC" 2>&1)
assert_not_contains "$_env" "$KEY=" "unsetenv removed $KEY from per-svc env"

test_summary
