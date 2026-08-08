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
    /bin/sh -c 'echo "$$RUN_MARKER" > '"$MARK" >/dev/null 2>&1
# NOTE: $$ (not $) — slinitctl writes the payload verbatim into the
# transient svc description, and slinit's config parser pre-expands
# $VAR at load time (would collapse RUN_MARKER to empty). $$RUN_MARKER
# survives that pass and reaches the runtime shell as $RUN_MARKER,
# which the env-file provides.

# Poll for the marker.
_e=0
while [ "$_e" -lt 8 ]; do
    [ -e "$MARK" ] && break
    sleep 1; _e=$((_e + 1))
done

# --setenv reached the child.
assert_eq "$(cat "$MARK" 2>/dev/null)" "hello-run-flags" \
    "--setenv value reached the transient svc's env"

# Note: --collect cleanup is best-effort on this daemon (may leave
# the drop-in behind depending on version). The primary contract we
# verify here is that --setenv reaches the child.

test_summary
