#!/bin/sh
# 18-private-tmp — `private-tmp = yes` should give the service its own
# /tmp namespace. Probe: have the service write a marker into /tmp, and
# from outside the namespace verify host /tmp does NOT see it.

SVC="acceptance-test-private-tmp"
COOKIE="acceptance-cookie-$$"

# Marker is written from inside the namespace into /tmp; on the host this
# path won't exist because the service's /tmp is a private mount.

trap 'svc_remove "$SVC"; rm -f /tmp/${COOKIE}' EXIT INT TERM

rm -f "/tmp/${COOKIE}"

svc_deploy "$SVC" <<EOF
type = process
private-tmp = yes
command = /bin/sh -c 'touch /tmp/${COOKIE}; while :; do sleep 60; done'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null 2>&1
wait_for_service "$SVC" "STARTED" 10 || true

_pid="$(slinitctl --system status "$SVC" 2>/dev/null | awk '/PID:/ {print $2; exit}')"

# From the host's perspective, /tmp/<cookie> must NOT appear.
sleep 1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "/tmp/${COOKIE}" ]; then
    echo "OK: cookie not visible in host /tmp (private-tmp working)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cookie leaked into host /tmp"
fi

# From inside the namespace (via /proc/<pid>/root) it MUST appear.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -n "$_pid" ] && [ -e "/proc/$_pid/root/tmp/${COOKIE}" ]; then
    echo "OK: cookie visible inside service namespace"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: cookie not found at /proc/$_pid/root/tmp/${COOKIE}"
fi

test_summary
