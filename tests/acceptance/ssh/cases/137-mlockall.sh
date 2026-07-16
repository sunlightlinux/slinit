#!/bin/sh
# 137-mlockall — checks that `mlockall = current+future` raises the
# service's RLIMIT_MEMLOCK to unlimited so the service can itself call
# mlockall(2)/mlock(2). We deliberately do NOT check /proc/PID/status
# VmLck: per POSIX ("Memory locks are ... automatically removed
# (unlocked) during an execve(2)."), the runner's own mlockall call is
# torn down before the exec'd service ever runs — VmLck on the exec'd
# task is expected to be zero. Rlimits do survive execve, and that's
# the durable resource-permission that `mlockall = ...` delivers.

SVC="${ACCEPTANCE_NS_PREFIX}mlk"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
mlockall = current+future
command = /bin/sh -c 'while :; do sleep 60; done'
restart = no
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "service reached STARTED"

# PID may briefly be absent from status output right after STARTED
# reports terminal — poll a couple of times so a late-suite SSH
# round-trip doesn't spuriously fail the assertion.
_pid=""
_i=0
while [ "$_i" -lt 5 ]; do
    _pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
    [ -n "$_pid" ] && [ "$_pid" != "0" ] && break
    sleep 0.2
    _i=$((_i + 1))
done

# /proc/PID/limits format:
#   "Max locked memory     unlimited unlimited bytes"
# Column 4 is Soft, column 5 is Hard. Both must be "unlimited".
_line=$(awk '/^Max locked memory/' "/proc/$_pid/limits" 2>/dev/null)
_soft=$(printf '%s' "$_line" | awk '{ print $(NF-2) }')
_hard=$(printf '%s' "$_line" | awk '{ print $(NF-1) }')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_soft" = "unlimited" ] && [ "$_hard" = "unlimited" ]; then
    echo "OK: RLIMIT_MEMLOCK soft=unlimited hard=unlimited (mlockall directive raised the cap)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: RLIMIT_MEMLOCK not raised — line='$_line' (soft='$_soft' hard='$_hard')"
fi

test_summary
