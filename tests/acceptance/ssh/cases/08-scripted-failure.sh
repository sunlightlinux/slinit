#!/bin/sh
# 08-scripted-failure — a scripted service whose command exits non-zero must
# end up in FAILED, and slinitctl is-failed must report that.

SVC="acceptance-test-failure"

trap 'svc_remove "$SVC"' EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = scripted
command = /bin/sh -c 'exit 7'
restart = false
EOF

# Don't wait for the service — we expect failure. --no-wait isn't enough
# (it would still wait for the start to *commit*), so we tolerate a
# non-zero exit from start itself.
slinitctl --system start "$SVC" >/dev/null 2>&1 || true

# Give the daemon a tick to settle the FAILED state.
_elapsed=0
while [ "$_elapsed" -lt 10 ]; do
    _s="$(svc_state "$SVC")"
    case "$_s" in
        FAILED|STOPPED) break ;;
    esac
    sleep 1
    _elapsed=$((_elapsed + 1))
done

# is-failed must exit 0 (i.e. the service is in a failed state).
assert_exit_code "slinitctl --system is-failed $SVC" 0 \
    "is-failed reports failure"

# is-started must NOT report started.
assert_exit_code "slinitctl --system is-started $SVC" 1 \
    "is-started reports not started"

test_summary
