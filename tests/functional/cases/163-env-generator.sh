#!/bin/sh
# Test: env-generator emits KEY=VALUE, slinit merges into child env.
# The service starts at boot BEFORE the test creates the generator
# script (buildEnv logs the missing binary and continues, so the
# child comes up with no extra env). We create the script and
# restart the service — the second buildEnv sees the script and
# populates the vars.
#
# Everything below the restart therefore rests on the restart having
# actually replaced the process. The case used to take that on trust:
# it ignored `slinitctl restart`'s exit status and never compared the
# PID, so any reason the restart did not happen surfaced as
# "SLINIT_EG_TEST missing from env" — the most misleading shape a
# failure here can take, because that env is then simply the boot
# process's env and the generator was never at fault. That is exactly
# how it failed in CI on 2026-10-05, and it could not be reproduced in
# 46 runs afterwards. The restart's status, the PID comparison and the
# diagnostics at the bottom are what will name the cause next time.
cat > /tmp/eg-gen.sh <<'GEN'
#!/bin/sh
echo "SLINIT_EG_TEST=eg-value-42"
echo "# comment ignored"
echo ""
echo "SLINIT_EG_TWO=second-var"
GEN
chmod +x /tmp/eg-gen.sh

_TESTS_RUN=$((_TESTS_RUN + 1))
if wait_for_service "eg-svc" "STARTED" 10; then
    echo "OK: eg-svc reached STARTED at boot"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: eg-svc never reached STARTED at boot — nothing to restart"
    test_summary
    exit 1
fi

_pid0=$(slinitctl --system status eg-svc 2>/dev/null | awk '/PID:/ {print $2; exit}')
echo "INFO: eg-svc boot PID is ${_pid0:-<none>}"

_rst=$(slinitctl --system restart eg-svc 2>&1)
_rst_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rst_rc" -eq 0 ]; then
    echo "OK: slinitctl restart eg-svc succeeded"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinitctl restart eg-svc exited $_rst_rc: $_rst"
fi

wait_for_service "eg-svc" "STARTED" 10

_pid=$(slinitctl --system status eg-svc 2>/dev/null | awk '/PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no live PID for eg-svc after restart"
    test_summary
    exit 1
fi
echo "OK: eg-svc has PID $_pid post-restart"

# The whole point of the restart is a fresh process, built by a buildEnv
# that can see the generator. Reading the boot process instead would
# make the env assertions below fail for a reason that has nothing to do
# with env-generator.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid0" ] && [ "$_pid" = "$_pid0" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: eg-svc still runs its boot PID $_pid0 — the restart did not" \
         "replace the process, so the env below is the boot env and says" \
         "nothing about env-generator"
else
    echo "OK: eg-svc was replaced ($_pid0 -> $_pid)"
fi

_env=$(tr '\0' '\n' < /proc/$_pid/environ 2>/dev/null)
_env_bad=0

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_env" | grep -q '^SLINIT_EG_TEST=eg-value-42$'; then
    echo "OK: SLINIT_EG_TEST landed from env-generator"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    _env_bad=1
    echo "FAIL: SLINIT_EG_TEST missing from env"
fi
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_env" | grep -q '^SLINIT_EG_TWO=second-var$'; then
    echo "OK: SLINIT_EG_TWO also landed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    _env_bad=1
    echo "FAIL: SLINIT_EG_TWO missing from env"
fi

# Only on failure, and only the three things that distinguish the
# candidate causes: the script slinit was asked to run, what it prints,
# and what slinit said about it.
if [ "$_env_bad" -ne 0 ]; then
    echo "DIAG: generator file:"
    ls -l /tmp/eg-gen.sh 2>&1 | sed 's/^/DIAG:   /'
    echo "DIAG: generator output:"
    /tmp/eg-gen.sh 2>&1 | sed 's/^/DIAG:   /'
    echo "DIAG: what slinit logged about it:"
    grep -i 'env-generator\|eg-svc' /run/slinit/catch-all.log 2>/dev/null |
        tail -12 | sed 's/^/DIAG:   /'
    echo "DIAG: env of PID $_pid (SLINIT_ vars only):"
    echo "$_env" | grep '^SLINIT_' | sed 's/^/DIAG:   /'
fi

test_summary
