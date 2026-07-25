#!/bin/sh
# Test: pass-environment (whitelist) + unset-environment (blacklist)
# both surface in the child's /proc/self/environ.
# Validates:
#   pass-environment  → only FIXTURE_KEEP survives in the child env,
#                       FIXTURE_DROP is dropped even though it was
#                       explicitly set by env-file.
#   unset-environment → FIXTURE_KEEP survives, FIXTURE_DROP removed.

# Seed the env-files that both services consume. Done in the test
# script (not baked into the fixture) so the target paths are guaranteed
# to exist at start.
cat > /tmp/188-envpass.env <<'EOF'
FIXTURE_KEEP=kept
FIXTURE_DROP=dropped
EOF

cat > /tmp/188-envunset.env <<'EOF'
FIXTURE_KEEP=kept
FIXTURE_DROP=dropped
EOF

# Stop + start both to make sure the env-files land before start.
slinitctl --system stop envpass-svc >/dev/null 2>&1
slinitctl --system stop envunset-svc >/dev/null 2>&1
slinitctl --system start envpass-svc
slinitctl --system start envunset-svc

wait_for_service "envpass-svc" "STARTED" 10
wait_for_service "envunset-svc" "STARTED" 10

# Both /proc/self/environ dumps use NUL as separator — replace with newline for grep.
_pass=$(tr '\0' '\n' < /tmp/188-pass-out 2>/dev/null)
_unset=$(tr '\0' '\n' < /tmp/188-unset-out 2>/dev/null)

# --- pass-environment: FIXTURE_KEEP survives, FIXTURE_DROP does not.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_pass" | grep -q '^FIXTURE_KEEP=kept$'; then
    echo "OK: pass-environment kept FIXTURE_KEEP"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: FIXTURE_KEEP missing under pass-environment filter"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if ! echo "$_pass" | grep -q '^FIXTURE_DROP='; then
    echo "OK: pass-environment excluded FIXTURE_DROP"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: FIXTURE_DROP leaked past pass-environment filter"
fi

# --- unset-environment: FIXTURE_KEEP survives, FIXTURE_DROP removed.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_unset" | grep -q '^FIXTURE_KEEP=kept$'; then
    echo "OK: unset-environment left FIXTURE_KEEP alone"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: FIXTURE_KEEP removed by unset-environment"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if ! echo "$_unset" | grep -q '^FIXTURE_DROP='; then
    echo "OK: unset-environment removed FIXTURE_DROP"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: FIXTURE_DROP not removed by unset-environment"
fi

test_summary
