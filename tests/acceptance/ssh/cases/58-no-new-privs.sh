#!/bin/sh
# 58-no-new-privs — verify PR_SET_NO_NEW_PRIVS reaches the child.
#
# `options = no-new-privs` alone is enough to force the runner-wrap
# path (pkg/process/exec.go needsRunnerWrap, post-tier5). slinit-runner
# then calls seccomp.EnsureNoNewPrivs() right before the AppArmor onexec
# switch, so /proc/PID/status NoNewPrivs MUST end up 1 regardless of
# whether any other sandbox/seccomp knob is set. Two sub-cases keep
# both wiring paths covered:
#
#   A. NoNewPrivs is the ONLY runner-wrap trigger. Regression check that
#      needsRunnerWrap()'s NoNewPrivs branch never gets dropped.
#   B. NoNewPrivs combined with private-tmp (sandbox triggers the wrap
#      for its own reasons). Regression check that the runner emits
#      NNP even when seccomp/hardening aren't the trigger.
#
# Both run a long-lived child so we can read /proc/PID/status.

SVC_PLAIN="acceptance-test-nnp-plain"
SVC_SANDBOX="acceptance-test-nnp-sandbox"

cleanup() {
    svc_remove "$SVC_PLAIN" "$SVC_SANDBOX"
}
trap cleanup EXIT INT TERM

# --- Sub-case A: standalone no-new-privs ---------------------------
# Service has no sandbox/seccomp/hardening knobs — only NoNewPrivs.
# needsRunnerWrap must still flip on, runner must still set NNP=1.
svc_deploy "$SVC_PLAIN" <<EOF
type = process
options = no-new-privs
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system --no-wait start "$SVC_PLAIN" >/dev/null 2>&1
wait_for_service "$SVC_PLAIN" "STARTED" 10 || true
assert_service_state "$SVC_PLAIN" "STARTED" "$SVC_PLAIN STARTED (standalone NNP)"

_pid=$(slinitctl --system status "$SVC_PLAIN" 2>/dev/null | awk '/PID:/ {print $NF; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_pid" in
    *[!0-9]*|"")
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: couldn't read standalone-NNP pid: '$_pid'"
        ;;
    *)
        echo "OK: standalone-NNP child PID = $_pid"
        ;;
esac

if [ -n "$_pid" ] && [ -r /proc/$_pid/status ]; then
    _nnp=$(awk '/^NoNewPrivs:/ {print $2}' /proc/$_pid/status 2>/dev/null)
    assert_eq "$_nnp" "1" "standalone NNP: /proc/$_pid/status NoNewPrivs == 1"
fi

svc_remove "$SVC_PLAIN"

# --- Sub-case B: no-new-privs + private-tmp (runner-wrap via sandbox)
# private-tmp would have triggered the wrap on its own; the assertion
# here is that the runner still emits NNP=1 alongside the sandbox
# setup. If wrapWithRunner ever stops propagating --no-new-privs when
# the wrap is driven by a different trigger, this fails.
svc_deploy "$SVC_SANDBOX" <<EOF
type = process
options = no-new-privs
private-tmp = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system --no-wait start "$SVC_SANDBOX" >/dev/null 2>&1
wait_for_service "$SVC_SANDBOX" "STARTED" 10 || true
assert_service_state "$SVC_SANDBOX" "STARTED" "$SVC_SANDBOX STARTED (sandbox + NNP)"

_pid=$(slinitctl --system status "$SVC_SANDBOX" 2>/dev/null | awk '/PID:/ {print $NF; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_pid" in
    *[!0-9]*|"")
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: couldn't read sandbox pid: '$_pid'"
        ;;
    *)
        echo "OK: sandbox child PID = $_pid"
        ;;
esac

if [ -n "$_pid" ] && [ -r /proc/$_pid/status ]; then
    _nnp=$(awk '/^NoNewPrivs:/ {print $2}' /proc/$_pid/status 2>/dev/null)
    assert_eq "$_nnp" "1" "sandbox + NNP: /proc/$_pid/status NoNewPrivs == 1"
fi

test_summary
