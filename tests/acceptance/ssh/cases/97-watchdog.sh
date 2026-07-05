#!/bin/sh
# 97-watchdog — `watchdog-timeout` kills a service that stops pinging
# and lets it come back via restart=on-failure.
#
# The service must first go READY by writing to its pipefd:3, then
# stop pinging. Slinit's watchdog timer fires after
# watchdog-timeout, the service is killed, and the on-failure
# restart brings it back with a fresh line in the start-count file.

SVC="${ACCEPTANCE_NS_PREFIX}watchdog"
STARTS="/tmp/acceptance-watchdog-starts"

cleanup() {
    svc_remove "$SVC"
    rm -f "$STARTS"
}
trap cleanup EXIT INT TERM
cleanup

# printf 'r' >&3 signals READY. After that we intentionally sit idle
# — watchdog-timeout=3s kicks in and slinit kills the process.
# restart=on-failure brings it back → the counter file gets a new
# line each time.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c "date +%s >> $STARTS; printf r >&3; while true; do sleep 60; done"
ready-notification = pipefd:3
watchdog-timeout = 3
restart = on-failure
restart-delay = 1
restart-limit-count = 5
restart-limit-interval = 60
EOF

slinitctl --system start "$SVC" 2>/dev/null
wait_for_service "$SVC" STARTED 10
assert_eq "$(svc_state "$SVC")" "STARTED" "watchdog svc initially STARTED"

# First start line must be present.
_TESTS_RUN=$((_TESTS_RUN + 1))
_first_lines=$(wc -l <"$STARTS" 2>/dev/null || echo 0)
if [ "$_first_lines" -ge 1 ]; then
    echo "OK: initial start recorded ($_first_lines line)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no initial start line — starts file: $(cat $STARTS 2>&1)"
    test_summary
    exit 0
fi

# Wait long enough for the watchdog to fire + the restart to spawn a
# new process. watchdog-timeout=3s + restart-delay=1s → ~4s per cycle.
sleep 8

_TESTS_RUN=$((_TESTS_RUN + 1))
_after_lines=$(wc -l <"$STARTS" 2>/dev/null || echo 0)
if [ "$_after_lines" -gt "$_first_lines" ]; then
    echo "OK: watchdog fired + restart cycled — $_first_lines → $_after_lines starts"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: only $_after_lines lines after wait (want > $_first_lines)"
fi

# The service should still be tracked in a non-STOPPED state (either
# STARTED after restart or STARTING). Confirm restart wasn't fatal.
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$(svc_state "$SVC")" in
    STARTED|STARTING)
        echo "OK: service is still alive after the watchdog cycle"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: state = $(svc_state "$SVC")"
        ;;
esac

test_summary
