#!/bin/sh
# Test: a read-only root at boot. Services that need to write under
# /var fail, each on its own and promptly; nothing hangs; a service
# writing to /run (tmpfs) works; boot completes and PID 1 answers.
#
# The .d/rootfs/sbin/init wrapper remounts / read-only before exec'ing
# slinit. boot waits-for (soft) four services: a scripted write to
# /var/lib, a state-directory under /var/lib, a logfile under /var/log,
# and a write to /run.

_state() { slinitctl --system status "$1" 2>/dev/null | awk '/State:/ {print $2; exit}'; }

_TESTS_RUN=$((_TESTS_RUN + 1))
if awk '$2 == "/" && $4 ~ /(^|,)ro(,|$)/ {f=1} END {exit !f}' /proc/mounts; then
    echo "OK: / is mounted read-only"
else
    echo "FAIL: / is not read-only; the case set-up did not take"; _TESTS_FAILED=$((_TESTS_FAILED + 1))
    grep ' / ' /proc/mounts
fi

wait_for_service boot STARTED 15
assert_eq "$(_state boot)" "STARTED" "boot completes on a read-only root"

for _s in ro-writer ro-state ro-logfile ro-run; do
    echo "$_s: $(_state "$_s")"
done
assert_eq "$(_state ro-run)" "STARTED" "the /run writer works"
assert_eq "$(cat /run/ro-run.stamp 2>/dev/null)" "ok" "its file is there"
assert_eq "$(_state ro-writer)" "STOPPED" "the /var writer is down"
_out=$(slinitctl --system is-failed ro-writer 2>&1); _rc=$?
assert_eq "$_rc" "0" "the /var writer is reported failed"
_st=$(_state ro-state)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st" in
    STARTING|STOPPING)
        echo "FAIL: ro-state stuck in $_st"; _TESTS_FAILED=$((_TESTS_FAILED + 1)) ;;
    *)  echo "OK: ro-state settled ($_st)" ;;
esac
_st=$(_state ro-logfile)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st" in
    STARTING|STOPPING)
        echo "FAIL: ro-logfile stuck in $_st"; _TESTS_FAILED=$((_TESTS_FAILED + 1)) ;;
    *)  echo "OK: ro-logfile settled ($_st)" ;;
esac

_out=$(timeout 5 slinitctl --system list 2>&1); _rc=$?
assert_eq "$_rc" "0" "slinitctl list answers"
echo "$_out"
grep -a "ro-\|read-only" /run/slinit/catch-all.log | cut -c1-250

test_summary
