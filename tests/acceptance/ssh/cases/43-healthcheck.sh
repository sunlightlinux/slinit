#!/bin/sh
# 43-healthcheck — `healthcheck-command` is invoked at
# `healthcheck-interval` (after `healthcheck-delay`) while the service
# is STARTED. Each non-zero exit increments a failure counter and runs
# `unhealthy-command`. After `healthcheck-max-failures` consecutive
# failures slinit issues Stop(false) on the service
# (pkg/service/process.go:395-400) which transitions through STOPPING
# to STOPPED. (`restart = true` covers unexpected process exits, not
# healthcheck-triggered stops — so the service stays down.)

SVC="acceptance-test-health"
UNH="/run/acceptance-test-health.unhealthy"

cleanup() {
    svc_remove "$SVC"
    rm -f "$UNH"
}
trap cleanup EXIT INT TERM

rm -f "$UNH"
: > "$UNH"

# Always-fail healthcheck, 1s interval, max-failures=2 → kill after ~2s.
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
healthcheck-command = /bin/sh -c 'exit 1'
healthcheck-interval = 1s
healthcheck-max-failures = 2
unhealthy-command = /bin/sh -c 'date +%s >> $UNH'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true
assert_service_state "$SVC" "STARTED" "$SVC STARTED"

# Wait long enough for 2 failed healthchecks + the stop transition.
sleep 5

_TESTS_RUN=$((_TESTS_RUN + 1))
_unh=$(wc -l < "$UNH" 2>/dev/null | tr -d ' ')
if [ -z "$_unh" ]; then _unh=0; fi
if [ "$_unh" -ge 2 ]; then
    echo "OK: unhealthy-command fired $_unh times (>=2 — max-failures reached)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: unhealthy-command fired only $_unh times (expected >=2)"
fi

# After max-failures, the healthcheck issues Stop(false). The service
# should be STOPPED or STOPPING (transition takes a moment).
_TESTS_RUN=$((_TESTS_RUN + 1))
_st=$(svc_state "$SVC")
case "$_st" in
    STOPPED|STOPPING|"")
        echo "OK: healthcheck stopped the service (now '$_st')"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: service still '$_st' after max-failures (expected STOPPED/STOPPING)"
        ;;
esac

# slinit-check static coverage for invalid healthcheck-max-failures.
_CHECKDIR="/tmp/acceptance-test-health-check"
mkdir -p "$_CHECKDIR"
trap '
    svc_remove "$SVC"
    rm -f "$UNH"
    rm -rf "$_CHECKDIR"
' EXIT INT TERM

cat > "$_CHECKDIR/svc-bad" <<'EOF2'
type = process
command = /bin/true
healthcheck-command = /bin/true
healthcheck-max-failures = -1
EOF2
_TESTS_RUN=$((_TESTS_RUN + 1))
# slinit-check resolves service NAMES against -d directories.
if slinit-check -d "$_CHECKDIR" svc-bad >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinit-check accepted healthcheck-max-failures = -1"
else
    echo "OK: slinit-check rejects healthcheck-max-failures = -1"
fi

test_summary
