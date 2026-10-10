#!/bin/sh
# 137-mlockall — checks that `mlockall = current+future` locks the
# service's memory and raises its RLIMIT_MEMLOCK to unlimited. Locks do
# not survive execve(2), so the runner preloads libslinit-mlock.so, which
# calls mlockall(2) inside the service: VmLck must be non-zero. The VM
# needs the library installed (<prefix>/lib/slinit/), or the service
# refuses to start.

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

# The PID slinit reports is the forked child, which is slinit-runner
# until it execs into the service. Measuring it before the exec reads
# the WRAPPER, and the wrapper gives a wrong answer in both directions:
# it raises RLIMIT_MEMLOCK and then calls mlockall(2) on itself, so a
# sample landing between the two shows an unlimited rlimit with
# VmLck=0 (what this case reported on ceres), and one landing after
# shows VmLck≈VmSize — around 1.2 GB, because the runner is a Go
# program and that is its runtime arena, not the service's memory.
# The functional twin 125 passed for its whole life on the second
# reading while the feature was broken.
#
# So wait for the exec, and never measure anything still called
# slinit-runner. Verified on ceres: comm=sh, VmLck=2624 kB with
# VmSize=2660 kB, library mapped, SLINIT_MLOCKALL_PID equal to the
# service's own pid.
_pid=""
_deadline=$(( $(date +%s) + 15 ))
while [ "$(date +%s)" -lt "$_deadline" ]; do
    _pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/^  PID:/ { print $2 }')
    if [ -n "$_pid" ] && [ "$_pid" != "0" ]; then
        case "$(cat "/proc/$_pid/comm" 2>/dev/null)" in
            slinit-runner) ;;          # still the wrapper; keep waiting
            "") ;;                     # vanished between the two reads
            *) break ;;
        esac
    fi
    sleep 0.5
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no post-exec PID for $SVC within 15s — the assertions below" \
         "would read /proc//… and report the feature broken"
    test_summary
    exit 1
fi
echo "OK: $SVC is past the runner's exec (pid $_pid, comm $(cat "/proc/$_pid/comm" 2>/dev/null))"

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

# The lock itself: slinit-runner preloads libslinit-mlock.so, which calls
# mlockall(2) inside the service's main process, so VmLck is non-zero.
_lck=$(awk '/^VmLck:/ { print $2 }' "/proc/$_pid/status" 2>/dev/null)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_lck" ] && [ "$_lck" -gt 0 ]; then
    echo "OK: service memory locked (VmLck=${_lck} kB)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: service memory not locked (VmLck='${_lck}')"
fi

test_summary
