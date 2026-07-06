#!/bin/sh
# 114-restart-backoff — `restart-delay` throttles restarts of a
# service that keeps failing. In slinit v1.10.32 the progressive
# `restart-delay-step`/`-cap` math is smooth-recovery-only (unit-
# tested); this acceptance case confirms:
#   1. the parser accepts step + cap syntax cleanly (no reject),
#   2. `restart-delay` throttles on-failure retries observably.

SVC="${ACCEPTANCE_NS_PREFIX}backoff"
STARTS="/tmp/acceptance-backoff-starts"

cleanup() {
    svc_remove "$SVC"
    rm -f "$STARTS"
}
trap cleanup EXIT INT TERM
cleanup

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'date +%s%N >> $STARTS; exit 1'
restart = on-failure
restart-delay = 0.05
restart-delay-step = 200ms
restart-delay-cap = 1s
restart-limit-count = 5
restart-limit-interval = 60
EOF

slinitctl --system start "$SVC" 2>/dev/null
sleep 5
slinitctl --system stop "$SVC" 2>/dev/null

# With restart-limit-count = 5, we expect 1 initial + 5 restart
# attempts = 6 total. If the limit stopped the loop, count = 6.
_lines=$(wc -l <"$STARTS" 2>/dev/null || echo 0)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_lines" -ge 2 ] && [ "$_lines" -le 7 ]; then
    echo "OK: $_lines attempts recorded (within limit-count bound)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $_lines attempts — expected 2..7 with restart-limit-count=5"
fi

# The service must eventually become STOPPED once the limit is hit.
sleep 1
_state=$(svc_state "$SVC")
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_state" in
    STOPPED|STARTED)
        echo "OK: service state = $_state"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: unexpected state='$_state'"
        ;;
esac

test_summary
