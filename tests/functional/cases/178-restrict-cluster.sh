#!/bin/sh
# Test: the full restrict-* cluster stacks and installs cleanly.
# Validates:
#   1. Every arg-checking BPF program (RestrictRealtime, Namespaces,
#      SUIDSGID, FileSystems, AddressFamilies) compiles and installs;
#      any compilation error would cause slinit-runner to abort before
#      exec.
#   2. The child sees Seccomp: 2 (SECCOMP_MODE_FILTER) after all five
#      filters are stacked. Proves the seccomp path executed rather
#      than being silently skipped.
#   3. restrict-suidsgid works in the stack, not just installs: the
#      service's `chmod 4755` on a file it owns fails with EPERM and the
#      file has no setuid bit afterwards. Seccomp: 2 alone could not
#      fail on a broken filter — any one of the five sets it — so this
#      is the probe a mutant (restrict-suidsgid = no) trips.
#
# Behavioural verification of the other filters (that the covered
# syscalls actually EPERM) lives in pkg/seccomp/restrict_test.go.

wait_for_service "restrict-svc" "STARTED" 15
assert_service_state "restrict-svc" "STARTED" "restrict-svc STARTED with 5-filter stack"

sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -f /tmp/178-seccomp ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /tmp/178-seccomp missing — body did not run"
    test_summary
    return
fi

# Printed on every run, pass or fail. This case went red on CI while 27
# others in the same shard passed — including 149-no-new-privs,
# 78-sandbox and 192-lsm-fail-closed, which all need slinit-runner, so the
# runner was working. It passes here with and without KVM. What the
# failure did not record is whether the body ran once or twice: a restart
# that re-ran the command would overwrite the marker, and the state at
# that moment is the fact worth having.
echo "--- diag: restrict-svc ---"
slinitctl --system status restrict-svc 2>&1 || true
echo "--- diag: marker contents ---"
cat /tmp/178-seccomp 2>&1 || true
echo "--- diag: runner present? ---"
ls -l /sbin/slinit-runner 2>&1 || true
echo "--- diag: end ---"

_mode=$(awk '{print $2; exit}' /tmp/178-seccomp)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_mode" = "2" ]; then
    echo "OK: child running under SECCOMP_MODE_FILTER (mode=2, filters stacked)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected Seccomp mode 2, got '$_mode'"
fi

# restrict-suidsgid, behaviourally. The body writes its rc file after
# the chmod, so wait for that rather than for the seccomp marker.
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -s /run/178-chmod-rc ] && break
    sleep 0.5
done
_chmod_rc=$(cat /run/178-chmod-rc 2>/dev/null)
_chmod_err=$(cat /run/178-chmod-err 2>/dev/null)
echo "  diag: chmod 4755 -> '$_chmod_rc' ($_chmod_err)"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_chmod_rc" ] && [ "$_chmod_rc" != "rc=0" ]; then
    echo "OK: chmod 4755 refused inside restrict-svc ($_chmod_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: chmod 4755 inside restrict-svc gave '$_chmod_rc', expected a failure"
fi
assert_contains "$_chmod_err" "not permitted" "the refusal is EPERM from restrict-suidsgid"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e /run/178-suid ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /run/178-suid was never created — the probe did not run"
elif [ -u /run/178-suid ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: /run/178-suid has the setuid bit — restrict-suidsgid let it through"
else
    echo "OK: /run/178-suid has no setuid bit"
fi

test_summary
