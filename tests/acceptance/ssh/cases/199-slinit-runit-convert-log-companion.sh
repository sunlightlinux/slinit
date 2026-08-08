#!/bin/sh
# 199-slinit-runit-convert-log-companion — the v2.1.5 headline
# feature: a runit sv dir with a `log/run` subdirectory produces
# TWO output files (primary + `-log` companion), with the primary
# gaining `log-type = pipe` and the companion carrying
# `consumer-of = <primary>`. The pair is what makes slinit's
# consumer-of validator accept the wiring.

WORK=/tmp/acc-199-runit-log
OUT=/tmp/acc-199-out
cleanup() { rm -rf "$WORK" "$OUT"; }
trap cleanup EXIT INT TERM

mkdir -p "$WORK/svc-logged/log" "$OUT"
cat > "$WORK/svc-logged/run" <<'EOF'
#!/bin/sh
exec /usr/bin/example-daemon
EOF
chmod +x "$WORK/svc-logged/run"

cat > "$WORK/svc-logged/log/run" <<'EOF'
#!/bin/sh
exec /usr/bin/vlogger -t svc-logged -p daemon
EOF
chmod +x "$WORK/svc-logged/log/run"

slinit-runit-convert --output-dir="$OUT" "$WORK/svc-logged" >/dev/null 2>&1

# Both files must exist.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$OUT/svc-logged" ] && [ -f "$OUT/svc-logged-log" ]; then
    echo "OK: both primary + companion emitted"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: missing files — $(ls "$OUT")"
fi

_primary=$(cat "$OUT/svc-logged")
_log=$(cat "$OUT/svc-logged-log")

assert_contains "$_primary" "log-type = pipe" "primary gains log-type = pipe"
assert_contains "$_log" "consumer-of = svc-logged" "companion consumer-of wired to primary"
assert_contains "$_log" "Auto-generated companion" "companion provenance comment"

# vlogger should get its absolute path from LookPath (v2.1.5 fix
# for slinit's execve-no-PATH-search). If /usr/bin/vlogger doesn't
# exist on the host, the tool leaves the name bare — accept either
# resolved or bare form so this case works on ceres regardless of
# whether socklog is installed.
assert_contains "$_log" "vlogger -t svc-logged" "companion command carries vlogger"

test_summary
