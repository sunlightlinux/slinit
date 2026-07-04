#!/bin/sh
# Test: slinit-svc-value persists per-service state across separate
# process invocations and exposes it via the OpenRC-shaped applets.
# Validates: file-per-key backing under $RC_SVCDIR/options/, exit
# codes, symlink dispatch (slinit-service_get_value → get behaviour),
# and the service_export "capture from env only when unstored" rule.

wait_for_service "boot" "STARTED" 10

# Install symlinks for the five applets. Real deployments would ship
# these as part of the package; the functional test wires them at
# runtime so the case is self-contained.
for applet in service_get_value get_options service_set_value \
              save_options service_export; do
    ln -sf /usr/bin/slinit-svc-value "/usr/bin/${applet}"
done

# Point the store at a scratch dir so we don't collide with the real
# slinit runtime state, and pick a service name.
export RC_SVCDIR=/tmp/svcvalue-test
export RC_SVCNAME=demo-svc
rm -rf "$RC_SVCDIR"

# --- Round trip via service_set_value / service_get_value ---

service_set_value listen_port 8443
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "${RC_SVCDIR}/options/${RC_SVCNAME}/listen_port" ]; then
    echo "OK: backing file created"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no backing file at ${RC_SVCDIR}/options/${RC_SVCNAME}/listen_port"
    ls -la "${RC_SVCDIR}/options/${RC_SVCNAME}/" 2>&1 || true
fi

value=$(service_get_value listen_port)
assert_eq "$value" "8443" "get returns stored value"

# No trailing newline — matches OpenRC. Length must equal the raw
# value (4 chars for "8443") plus zero for the newline the shell
# capture added and would strip.
raw=$(cat "${RC_SVCDIR}/options/${RC_SVCNAME}/listen_port")
assert_eq "$raw" "8443" "raw file has no trailing newline"

# --- Miss → exit 1 ---

_TESTS_RUN=$((_TESTS_RUN + 1))
if service_get_value nonexistent >/dev/null 2>&1; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: get on missing key should exit non-zero"
else
    echo "OK: get on missing key exits non-zero"
fi

# --- Empty VALUE deletes ---

service_set_value listen_port  # no value
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e "${RC_SVCDIR}/options/${RC_SVCNAME}/listen_port" ]; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: empty-value set should delete the file"
else
    echo "OK: empty-value set removes the key"
fi

# --- Legacy aliases behave identically ---

save_options greeting hello
value=$(get_options greeting)
assert_eq "$value" "hello" "get_options alias returns save_options value"

# --- service_export: capture only when unstored ---

export EXPORT_A=captured-a
export EXPORT_B=captured-b
service_set_value EXPORT_A "already-there"

service_export EXPORT_A EXPORT_B

# EXPORT_A was pre-stored, must NOT be clobbered.
value=$(service_get_value EXPORT_A)
assert_eq "$value" "already-there" "export leaves already-stored key alone"

# EXPORT_B captured from env.
value=$(service_get_value EXPORT_B)
assert_eq "$value" "captured-b" "export captures unstored key from env"

# --- SLINIT_SERVICENAME fallback ---

unset RC_SVCNAME
export SLINIT_SERVICENAME=slinit-native
service_set_value key1 val1
value=$(service_get_value key1)
assert_eq "$value" "val1" "SLINIT_SERVICENAME fallback works"
# And lives under a different service dir than RC_SVCNAME did.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "${RC_SVCDIR}/options/slinit-native/key1" ]; then
    echo "OK: fallback picks a distinct service dir"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no file under slinit-native"
fi

# --- No service name → exit 2 (bad usage) ---

unset SLINIT_SERVICENAME
_TESTS_RUN=$((_TESTS_RUN + 1))
service_get_value anything >/dev/null 2>/tmp/svcvalue.err
rc=$?
if [ "$rc" = "2" ]; then
    echo "OK: missing service name yields exit 2"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: rc=$rc, want 2 — stderr: $(cat /tmp/svcvalue.err)"
fi

# Cleanup so a rerun / other cases don't inherit stale state.
rm -rf "$RC_SVCDIR"
for applet in service_get_value get_options service_set_value \
              save_options service_export; do
    rm -f "/usr/bin/${applet}"
done

test_summary
