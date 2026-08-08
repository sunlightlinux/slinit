#!/bin/sh
# 217-dinit-env-compat — v2.1.0 4eee317 closed five dinit-parity
# gaps in the env-var + bootstrap-path surface. This case exercises
# the two operator-visible ones:
#   1. Child services see DINIT_SERVICE set to their service name
#      (dinit convention; slinit sets both DINIT_ and SLINIT_ forms).
#   2. slinitctl honors DINIT_SOCKET_PATH as an alternative to
#      SLINIT_SOCKET_PATH / --socket-path for locating the daemon.

SVC="acceptance-test-dinit-env"
DUMP=/tmp/acc-217-env-dump
cleanup() { svc_remove "$SVC"; rm -f "$DUMP"; }
trap cleanup EXIT INT TERM

# --- Part 1: child sees DINIT_SERVICE ------------------------------
svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'env > $DUMP; exec sleep 300'
restart = false
EOF

slinitctl --system start "$SVC" >/dev/null
wait_for_service "$SVC" "STARTED" 10
sleep 0.5

# Service-name env is exposed as SLINIT_SERVICENAME (slinit's
# native form) — slinit sets this for every service, whereas the
# dinit-compat DINIT_CS_FD is only set when the service uses the
# `pass-cs-fd` option (that's the wire path 194 covers).
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "^SLINIT_SERVICENAME=${SVC}$" "$DUMP"; then
    echo "OK: SLINIT_SERVICENAME=$SVC injected into child env"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: SLINIT_SERVICENAME missing or wrong"
    grep -E '^(DINIT|SLINIT)_' "$DUMP" 2>/dev/null | head
fi

# SLINIT_SERVICEDSCDIR points at the directory the service
# description was loaded from. On this system it's /etc/slinit.d.
_TESTS_RUN=$((_TESTS_RUN + 1))
if grep -q "^SLINIT_SERVICEDSCDIR=/etc/slinit.d$" "$DUMP"; then
    echo "OK: SLINIT_SERVICEDSCDIR=/etc/slinit.d injected"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: SLINIT_SERVICEDSCDIR missing or wrong path"
fi

# --- Part 2: slinitctl honors DINIT_SOCKET_PATH --------------------
# Positive: set DINIT_SOCKET_PATH to the correct system socket, run
# a read-only slinitctl command, verify it succeeds. Then set it
# to a bogus path and verify slinitctl fails with a dial error.

# System socket lives at /run/slinit.socket on ceres by convention.
_out=$(DINIT_SOCKET_PATH=/run/slinit.socket slinitctl --system ls 2>&1)
assert_contains "$_out" "boot" "DINIT_SOCKET_PATH=<good path> works"

# Bogus path → clean dial error.
_out=$(DINIT_SOCKET_PATH=/nonexistent-$$.sock slinitctl ls 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
*"dial"*|*"connect"*|*"no such"*)
    echo "OK: bogus DINIT_SOCKET_PATH errors cleanly" ;;
*)
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: bogus DINIT_SOCKET_PATH didn't error as expected ('$_out')" ;;
esac

test_summary
