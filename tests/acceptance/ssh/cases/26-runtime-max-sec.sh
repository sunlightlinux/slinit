#!/bin/sh
# 26-runtime-max-sec — hard cap on a service's running time. After the cap
# slinit must terminate the process. Probe: deploy a sleep-forever process
# with runtime-max-sec = 3, restart=false; after ~5s the service must no
# longer be STARTED and the originally-tracked PID must be gone.

SVC="acceptance-test-runtime-max"

trap 'svc_remove "$SVC"' EXIT INT TERM

# runtime-max-sec is parsed by Go's time.ParseDuration — bare integers are
# rejected; a unit suffix is required (`3s`, `1m30s`, etc).
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
runtime-max-sec = 3s
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC initial STARTED"

_pid=$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -d "/proc/$_pid" ]; then
    echo "OK: tracking pid $_pid before cap"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no live PID for $SVC"
    test_summary
    exit 1
fi

# Wait for the cap to fire. Margin: 3s cap + a beat for slinit to signal +
# the kernel to reap. 7s is comfortable.
sleep 7

# The originally-tracked PID must be gone (slinit signalled it).
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -d "/proc/$_pid" ]; then
    echo "OK: pid $_pid terminated by runtime-max-sec cap"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: pid $_pid still alive after cap"
fi

# Service must not still be STARTED.
_st=$(svc_state "$SVC")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st" in
    STARTED)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $SVC still STARTED past the cap"
        ;;
    *)
        echo "OK: $SVC moved out of STARTED (now '$_st')"
        ;;
esac

test_summary
