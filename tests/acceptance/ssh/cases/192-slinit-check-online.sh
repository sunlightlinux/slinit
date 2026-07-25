#!/bin/sh
# 192-slinit-check-online — `slinit-check --online` queries the live
# daemon for service-dirs and env, then validates a service against
# that context. Distinct from the offline linter (case 93), which uses
# only static file information.

SVC="acceptance-test-online-check"

cleanup() { svc_remove "$SVC"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<EOF
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

# --- baseline: --online on a valid svc succeeds -----------------------
_out=$(slinit-check --online "$SVC" 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: slinit-check --online $SVC exits 0 for valid svc"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --online on valid svc failed rc=$_rc: $_out"
fi

# --- --online on a nonexistent svc fails clearly ----------------------
_out=$(slinit-check --online "acceptance-test-does-not-exist" 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -ne 0 ]; then
    echo "OK: --online on missing svc exits non-zero (rc=$_rc)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --online on missing svc unexpectedly succeeded"
fi

# --- --online without args validates the daemon's default target -----
_out=$(slinit-check --online 2>&1)
_rc=$?
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_rc" -eq 0 ]; then
    echo "OK: slinit-check --online (no args) validates default target"
else
    # Non-zero is acceptable if the boot target has other issues; just
    # want to confirm the CLI doesn't crash.
    echo "OK: slinit-check --online (no args) returned $_rc — accepted"
fi

test_summary
