#!/bin/sh
# 198-slinit-runit-convert-basic — smallest happy path for the
# v2.1.4 converter: a fake `/etc/sv/<name>/run` script gets
# converted to a slinit service file with type=/command=/restart=
# populated. Covers the direct-extraction path (no chpst, no
# metachars, no setup logic).

WORK=/tmp/acc-198-runit
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

mkdir -p "$WORK/svc-example"
cat > "$WORK/svc-example/run" <<'EOF'
#!/bin/sh
exec 2>&1
exec /usr/bin/example-daemon --config /etc/example.conf
EOF
chmod +x "$WORK/svc-example/run"

_out=$(slinit-runit-convert "$WORK/svc-example" 2>/dev/null)

assert_contains "$_out" "type = process" "type = process emitted"
assert_contains "$_out" "command = /usr/bin/example-daemon --config /etc/example.conf" "command extracted directly"
assert_contains "$_out" "restart = yes" "runit default restart preserved"
assert_contains "$_out" "restart-delay = 1" "runit default cooldown preserved"
assert_contains "$_out" "working-dir = $WORK/svc-example" "working-dir defaults to sv dir (runsv chdir compat)"

# The provenance comment must name the source path so operators
# can trace a converted file back to its origin.
assert_contains "$_out" "Converted from runit service dir: $WORK/svc-example" "provenance comment present"

test_summary
