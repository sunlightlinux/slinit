#!/bin/sh
# Test: supplementary-groups= installs a supplementary group set via
# setgroups(2) before the run-as drop. Validated by reading the
# child's /proc/PID/status Groups: line and checking all configured
# GIDs are present. Numeric fallback (no /etc/group entry required)
# is the whole point of the resolver — the test picks GIDs {27,100,
# 500} which are unlikely to exist as named groups on a minimal VM.

# The sup-svc service is injected from 140-supplementary-groups.d/
wait_for_service "sup-svc" "STARTED" 10
assert_service_state "sup-svc" "STARTED" "sup-svc reached STARTED"

_pid=""
_i=0
while [ "$_i" -lt 5 ]; do
    _pid=$(slinitctl --system status sup-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')
    [ -n "$_pid" ] && [ "$_pid" != "0" ] && break
    sleep 0.2
    _i=$((_i + 1))
done

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: could not resolve PID for sup-svc"
    test_summary
    return
fi
echo "OK: sup-svc pid=$_pid"

# /proc/PID/status Groups: line is tab-separated on some kernels
# ("Groups:\t27 100 500 "), space-separated on others. Extract just
# the numeric fields via awk so the case-glob check below doesn't
# need to worry about which whitespace the kernel picked.
_groups=$(awk '/^Groups:/ { for (i=2; i<=NF; i++) printf "%s ", $i }' "/proc/$_pid/status" 2>/dev/null)

for want in 27 100 500; do
    _TESTS_RUN=$((_TESTS_RUN + 1))
    case " $_groups" in
        *" $want "*)
            echo "OK: Groups: contains $want"
            ;;
        *)
            _TESTS_FAILED=$((_TESTS_FAILED + 1))
            echo "FAIL: Groups: missing $want (fields: '$_groups')"
            ;;
    esac
done

# Also confirm the run-as UID actually took effect — otherwise the
# setgroups path wouldn't have been exercised at all (guards against
# a regression where run-as is silently ignored).
_uid=$(awk '/^Uid:/ { print $2; exit }' "/proc/$_pid/status" 2>/dev/null)
assert_eq "$_uid" "65534" "child real UID = 65534 (nobody)"

test_summary
