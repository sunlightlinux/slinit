#!/bin/sh
# 86-slinit-svc-value — per-service persistent key=value store.
#
# Exercises symlink dispatch (service_get_value / service_set_value /
# service_export + legacy get_options / save_options aliases), the
# empty-value delete rule, and the SLINIT_SERVICENAME env fallback.

SVCDIR="/tmp/acceptance-svcvalue"
cleanup() {
    rm -rf "$SVCDIR" /tmp/svcvalue.err
}
trap cleanup EXIT INT TERM
cleanup

# Isolate the store away from /run/slinit so a leaked file can't
# collide with anything real.
export RC_SVCDIR="$SVCDIR"
export RC_SVCNAME=acceptance-test-svcvalue

# --- Round trip via service_set_value / service_get_value ---
service_set_value listen_port 8443
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$SVCDIR/options/$RC_SVCNAME/listen_port" ]; then
    echo "OK: backing file created at $SVCDIR/options/$RC_SVCNAME/listen_port"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no backing file"
fi

assert_eq "$(service_get_value listen_port)" "8443" "get returns stored value"

# Raw file must NOT carry a trailing newline (OpenRC contract).
raw=$(cat "$SVCDIR/options/$RC_SVCNAME/listen_port")
assert_eq "$raw" "8443" "raw file has no trailing newline"

# Miss → exit 1.
_TESTS_RUN=$((_TESTS_RUN + 1))
if service_get_value nonexistent >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: missing key should exit non-zero"
else
    echo "OK: missing key exits non-zero"
fi

# Empty VALUE deletes.
service_set_value listen_port
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "$SVCDIR/options/$RC_SVCNAME/listen_port" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: empty-value set should delete the file"
else
    echo "OK: empty-value set removes the key"
fi

# Legacy save_options / get_options alias.
save_options greeting hello
assert_eq "$(get_options greeting)" "hello" "get_options alias returns save_options value"

# service_export: only captures when unstored.
export EXPORT_A=captured-a EXPORT_B=captured-b
service_set_value EXPORT_A already-there
service_export EXPORT_A EXPORT_B
assert_eq "$(service_get_value EXPORT_A)" "already-there" "export leaves already-stored key alone"
assert_eq "$(service_get_value EXPORT_B)" "captured-b" "export captures unstored key from env"

# SLINIT_SERVICENAME fallback.
unset RC_SVCNAME
export SLINIT_SERVICENAME=acceptance-test-svcvalue-alt
service_set_value key1 val1
assert_eq "$(service_get_value key1)" "val1" "SLINIT_SERVICENAME fallback works"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$SVCDIR/options/acceptance-test-svcvalue-alt/key1" ]; then
    echo "OK: fallback picks a distinct service dir"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no file under acceptance-test-svcvalue-alt"
fi

# Missing service name → exit 2.
unset SLINIT_SERVICENAME
_TESTS_RUN=$((_TESTS_RUN + 1))
service_get_value anything >/dev/null 2>/tmp/svcvalue.err
rc=$?
if [ "$rc" = "2" ]; then
    echo "OK: missing service name yields exit 2"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rc=$rc, want 2 — $(cat /tmp/svcvalue.err)"
fi

test_summary
