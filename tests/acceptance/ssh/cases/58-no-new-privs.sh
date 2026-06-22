#!/bin/sh
# 58-no-new-privs — verify PR_SET_NO_NEW_PRIVS reaches the child.
#
# Two paths, both important:
#
#   A. `options = no-new-privs` alone. Today this hits the unwrapped exec
#      path → pkg/process/attrs.go:345 applyNoNewPrivs returns an
#      error (parent can't set NNP on a peer process; needs child-side
#      prctl). Comment claims "TODO: implement via Cloneflags or a
#      small C helper". Probe the actual observable behavior so we
#      catch the day someone implements it: read NoNewPrivs from
#      /proc/PID/status.
#
#   B. `options = no-new-privs` combined with ANY runner-wrap trigger
#      (here: private-tmp = yes). The runner (cmd/slinit-runner/main.go)
#      calls seccomp.EnsureNoNewPrivs() before exec; NNP MUST end up 1
#      in /proc/PID/status. If this regresses, sandboxes silently
#      lose the no-new-privs guarantee.
#
# Both sub-cases run the child as long-lived so we can read /proc.

SVC_PLAIN="acceptance-test-nnp-plain"
SVC_SANDBOX="acceptance-test-nnp-sandbox"

cleanup() {
    svc_remove "$SVC_PLAIN" "$SVC_SANDBOX"
}
trap cleanup EXIT INT TERM

# --- Sub-case A: standalone no-new-privs ---------------------------
svc_deploy "$SVC_PLAIN" <<EOF
type = process
options = no-new-privs
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system --no-wait start "$SVC_PLAIN" >/dev/null 2>&1
wait_for_service "$SVC_PLAIN" "STARTED" 10 || true

_state=$(svc_state "$SVC_PLAIN")
_pid=$(slinitctl --system status "$SVC_PLAIN" 2>/dev/null | awk '/PID:/ {print $NF; exit}')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_state" = "STARTED" ] && [ -n "$_pid" ] && [ "$_pid" -gt 0 ] 2>/dev/null; then
    echo "OK: $SVC_PLAIN STARTED with pid $_pid (parent-side NNP stub did not fail the start)"
    _nnp=$(awk '/^NoNewPrivs:/ {print $2}' /proc/$_pid/status 2>/dev/null)
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case "$_nnp" in
        0)
            # Expected with the current stub — document it.
            echo "OK: /proc/$_pid/status NoNewPrivs=0 (stub: parent-side NNP not applied — see attrs.go:345 TODO)"
            ;;
        1)
            # Someone implemented it — congrats, but flag so the
            # comment in attrs.go gets updated.
            echo "OK: /proc/$_pid/status NoNewPrivs=1 (NNP now applied on the unwrapped path — update attrs.go comment!)"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: unexpected NoNewPrivs value '$_nnp' for pid $_pid"
            ;;
    esac
elif [ "$_state" = "STARTED" ]; then
    # Started but PID lookup failed — protocol issue, not a feature issue.
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: STARTED but pid not readable from status (pid='$_pid')"
else
    # If the stub fails the start, that's a different (worse) outcome
    # we want to surface — service should still come up.
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $SVC_PLAIN didn't reach STARTED (got '$_state') — stub fails the whole exec?"
fi

svc_remove "$SVC_PLAIN"

# --- Sub-case B: no-new-privs + private-tmp (runner-wrap path) ----
# private-tmp triggers sandboxActive() → needsRunnerWrap() → exec
# goes through slinit-runner, which calls seccomp.EnsureNoNewPrivs().
# Expected: NoNewPrivs=1 in /proc/PID/status.
svc_deploy "$SVC_SANDBOX" <<EOF
type = process
options = no-new-privs
private-tmp = yes
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system --no-wait start "$SVC_SANDBOX" >/dev/null 2>&1
wait_for_service "$SVC_SANDBOX" "STARTED" 10 || true
assert_service_state "$SVC_SANDBOX" "STARTED" "$SVC_SANDBOX STARTED (sandbox path)"

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
    assert_eq "$_nnp" "1" "runner-wrapped sandbox: /proc/$_pid/status NoNewPrivs == 1"
fi

# Bonus: Seccomp not set on this sandbox (we didn't ask for one), but
# the runner-wrap MUST still set NNP — NNP isn't optional on the
# wrapped path because seccomp needs it.

test_summary
