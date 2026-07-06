#!/bin/sh
# 107-start-timeout — scripted service that runs past `start-timeout`
# gets killed and marked FAILED.

SVC="${ACCEPTANCE_NS_PREFIX}starttmo"

cleanup() {
    svc_remove "$SVC"
}
trap cleanup EXIT INT TERM
cleanup

# Scripted service that sleeps 30s — well past our 3s timeout.
svc_deploy "$SVC" <<EOF
type = scripted
command = /bin/sh -c 'sleep 30'
start-timeout = 3
restart = no
EOF

_start=$(date +%s)
slinitctl --system start "$SVC" 2>/dev/null &
_wait_pid=$!

# Poll until the daemon marks it terminal.
_i=0
while [ "$_i" -lt 20 ]; do
    case "$(svc_state "$SVC")" in
        STOPPED|"")
            break
            ;;
    esac
    _i=$((_i + 1))
    sleep 0.5
done
wait "$_wait_pid" 2>/dev/null || true
_elapsed=$(( $(date +%s) - _start ))

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$(svc_state "$SVC")" in
    STOPPED)
        echo "OK: service reached STOPPED after start-timeout"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: state = $(svc_state "$SVC")"
        ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_elapsed" -le 8 ]; then
    echo "OK: timeout fired within ${_elapsed}s (<= 8s budget)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: elapsed ${_elapsed}s — timeout not honored"
fi

test_summary
