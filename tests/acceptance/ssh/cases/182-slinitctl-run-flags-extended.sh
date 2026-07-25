#!/bin/sh
# 182-slinitctl-run-flags-extended — `slinitctl run` supports several
# systemd-run-style flags beyond bare `--unit`. Verify --collect (auto
# cleanup) + --property (arbitrary key=value applied at load time) +
# --setenv (env for the transient svc) round-trip.

MARK=/run/acceptance-run-flags.mark
UNIT="acceptance-test-runflags-$$"

cleanup() {
    slinitctl --system --ignore-unstarted stop "$UNIT" 2>/dev/null || true
    slinitctl --system unload "$UNIT" 2>/dev/null || true
    rm -f "$MARK" "/run/slinit.d/${UNIT}" 2>/dev/null
}
trap cleanup EXIT INT TERM

rm -f "$MARK"

# --setenv passes RUN_MARKER; the shell command writes it to the marker.
# --property sets a directive on the transient svc file (nice=10 is
# innocuous and observable via the generated /run/slinit.d/<unit> file).
slinitctl --system run \
    --unit "$UNIT" \
    --collect \
    --setenv RUN_MARKER=hello-run-flags \
    --property nice=10 \
    -- \
    /bin/sh -c 'echo "$RUN_MARKER" > '"$MARK" >/dev/null 2>&1

# Poll for the marker.
_e=0
while [ "$_e" -lt 8 ]; do
    [ -e "$MARK" ] && break
    sleep 1; _e=$((_e + 1))
done

# --setenv reached the child.
assert_eq "$(cat "$MARK" 2>/dev/null)" "hello-run-flags" \
    "--setenv value reached the transient svc's env"

# --collect removed the drop-in after stop.
_e=0
while [ "$_e" -lt 5 ]; do
    [ ! -e "/run/slinit.d/${UNIT}" ] && break
    sleep 1; _e=$((_e + 1))
done
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e "/run/slinit.d/${UNIT}" ]; then
    echo "OK: --collect cleaned up drop-in"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: --collect did not remove /run/slinit.d/${UNIT}"
fi

test_summary
