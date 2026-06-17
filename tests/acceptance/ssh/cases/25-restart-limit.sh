#!/bin/sh
# 25-restart-limit — `restart-limit-count = N` + `restart-limit-interval = T`:
# if the service restarts more than N times within T seconds, slinit gives up
# and parks the service in a non-running state. Probe: a process that exits 1
# immediately, restart=true, count=2 interval=10 — after a beat, the state
# must be one of {STOPPED, FAILED} (i.e. NOT actively cycling and not STARTED).

SVC="acceptance-test-rlimit"

trap 'svc_remove "$SVC"' EXIT INT TERM

# Deliberately fast failure so we burn through the budget quickly.
# restart-delay is small so the test does not need to wait long.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'exit 1'
restart = true
restart-limit-count = 2
restart-limit-interval = 10
restart-delay = 0.2
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1

# Give it ~4s to exhaust 2 restarts and settle. With restart-delay=0.2s and
# instant exit, two restart cycles complete in well under a second.
_e=0
while [ "$_e" -lt 8 ]; do
    _st=$(svc_state "$SVC")
    case "$_st" in
        STOPPED|FAILED) break ;;
    esac
    sleep 1
    _e=$((_e + 1))
done

_st=$(svc_state "$SVC")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_st" in
    STOPPED|FAILED)
        echo "OK: $SVC settled at '$_st' after restart-limit exhausted"
        ;;
    STARTED)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $SVC unexpectedly STARTED — limit not enforced"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $SVC stuck at '$_st' — restart loop not bounded"
        ;;
esac

test_summary
