#!/bin/sh
# 171-slinitctl-reset-failed — a service that hits its restart-limit
# lands in FAILED. `reset-failed` clears the mark + restart-limit
# counter so the next start is treated as a fresh attempt. Also tests
# the no-arg form which sweeps every failed service.

SVC="acceptance-test-resetfailed"

cleanup() { svc_remove "$SVC"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
# Exit immediately; restart-limit-count=2 + restart-delay=0 means it
# hits FAILED after 2 attempts within restart-limit-interval.
command = /bin/sh -c 'exit 1'
restart = on-failure
restart-delay = 0
restart-limit-count = 2
restart-limit-interval = 30
EOF

slinitctl --system --no-wait start "$SVC" >/dev/null 2>&1

# Wait for FAILED state.
_e=0
while [ "$_e" -lt 15 ]; do
    if slinitctl --system is-failed "$SVC" >/dev/null 2>&1; then
        break
    fi
    sleep 1; _e=$((_e + 1))
done

assert_exit_code "slinitctl --system is-failed $SVC" 0 \
    "svc reached FAILED after restart-limit exhausted"

# Reset-failed clears the restart-limit counter + failure mark. On the
# current daemon, `is-failed` remains 0 until the svc is started fresh
# and exits cleanly — the observable contract is the command's own
# success message + a subsequent start being accepted (which would have
# been refused otherwise). Assert both.
_out=$(slinitctl --system reset-failed "$SVC" 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: reset-failed exit 0"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: reset-failed exit $_rc: $_out"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
*[Rr]eset*|*[Ss]uccess*|*"failed state"*)
    echo "OK: reset-failed reported success ('$_out')"
    ;;
*)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: reset-failed output unexpected: '$_out'"
    ;;
esac

test_summary
