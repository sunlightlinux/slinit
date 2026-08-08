#!/bin/sh
# 206-slinit-runit-convert — v2.1.4 + v2.1.5 headline converter.
# The v2.1.5 1:1 refactor adds finish/check/down/sv-check/log-run
# auto-detection and companion `-log` file emission with
# consumer-of + log-type=pipe. This test exercises the log
# companion path since it's the biggest win.

WORK=/tmp/rconv
OUT=/tmp/rconv-out
mkdir -p "$WORK/svc/log" "$OUT"

# Primary service with a simple exec + a companion log/run.
printf '#!/bin/sh\nexec /usr/bin/example-daemon\n' > "$WORK/svc/run"
printf '#!/bin/sh\nexec cat\n' > "$WORK/svc/log/run"
chmod +x "$WORK/svc/run" "$WORK/svc/log/run"

slinit-runit-convert --output-dir="$OUT" "$WORK/svc" >/dev/null 2>&1

# Both files landed.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$OUT/svc" ] && [ -f "$OUT/svc-log" ]; then
    echo "OK: primary + companion emitted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: missing files ($(ls "$OUT"))"
fi

# Primary carries the v2.1.5 log-type = pipe pairing directive
# so slinit's consumer-of validator accepts the wiring.
_primary=$(cat "$OUT/svc")
assert_contains "$_primary" "log-type = pipe" "primary carries log-type = pipe"
assert_contains "$_primary" "working-dir = $WORK/svc" "working-dir defaults to sv dir (runsv chdir compat)"

# Companion consumer-of points at the primary.
_log=$(cat "$OUT/svc-log")
assert_contains "$_log" "consumer-of = svc" "companion consumer-of wired to primary"
assert_contains "$_log" "Auto-generated companion" "companion has provenance comment"

# The provenance comment on the primary names the source dir.
assert_contains "$_primary" "Converted from runit service dir: $WORK/svc" "primary provenance comment present"

test_summary
