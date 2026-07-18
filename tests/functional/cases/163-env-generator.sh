#!/bin/sh
# Test: env-generator emits KEY=VALUE, slinit merges into child env.
# The service starts at boot BEFORE the test creates the generator
# script (buildEnv logs the missing binary and continues, so the
# child comes up with no extra env). We create the script and
# restart the service — the second buildEnv sees the script and
# populates the vars.
cat > /tmp/eg-gen.sh <<'GEN'
#!/bin/sh
echo "SLINIT_EG_TEST=eg-value-42"
echo "# comment ignored"
echo ""
echo "SLINIT_EG_TWO=second-var"
GEN
chmod +x /tmp/eg-gen.sh

wait_for_service "eg-svc" "STARTED" 10
slinitctl --system restart eg-svc >/dev/null 2>&1
wait_for_service "eg-svc" "STARTED" 10

_pid=$(slinitctl --system status eg-svc 2>/dev/null | awk '/PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no live PID for eg-svc after restart"
    test_summary
    exit 0
fi
echo "OK: eg-svc has PID $_pid post-restart"

_env=$(tr '\0' '\n' < /proc/$_pid/environ 2>/dev/null)

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_env" | grep -q '^SLINIT_EG_TEST=eg-value-42$'; then
    echo "OK: SLINIT_EG_TEST landed from env-generator"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: SLINIT_EG_TEST missing from env"
fi
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_env" | grep -q '^SLINIT_EG_TWO=second-var$'; then
    echo "OK: SLINIT_EG_TWO also landed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: SLINIT_EG_TWO missing from env"
fi

test_summary
