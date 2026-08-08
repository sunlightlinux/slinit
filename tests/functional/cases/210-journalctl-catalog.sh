#!/bin/sh
# 210-journalctl-catalog — v2.1.8 Group D: pkg/catalog systemd-
# compatible .catalog parser + --list-catalog / --dump-catalog /
# --update-catalog. Uses --root prefix so we can drop a synthetic
# catalog file into /tmp/... without touching the guest's real
# /usr/share.

wait_for_service "boot" "STARTED" 15

ROOT=/tmp/cat-root
mkdir -p "$ROOT/usr/share/slinit-catalog"

# Well-formed catalog entry.
cat > "$ROOT/usr/share/slinit-catalog/probe.catalog" <<'EOF'
-- deadbeefcafedeadbeefcafedeadbe01
Subject: Functional-test catalog probe
Defined-By: slinit-functional
Support: https://example.org/support

Long descriptive body.
Two lines to prove multi-line round-trip.
EOF

# --list-catalog finds the ID.
_out=$(slinit-journalctl --root="$ROOT" --list-catalog 2>&1)
assert_contains "$_out" "deadbeefcafedeadbeefcafedeadbe01" "--list-catalog finds our ID"

# --dump-catalog carries header + body verbatim.
_out=$(slinit-journalctl --root="$ROOT" --dump-catalog 2>&1)
assert_contains "$_out" "-- deadbeefcafedeadbeefcafedeadbe01" "--dump-catalog has '-- ID' header"
assert_contains "$_out" "Subject: Functional-test catalog probe" "dump has Subject line"
assert_contains "$_out" "Defined-By: slinit-functional" "dump title-cases header (Defined-By)"
assert_contains "$_out" "Long descriptive body" "dump carries body"

# --update-catalog writes the gob-compiled cache.
slinit-journalctl --root="$ROOT" --update-catalog >/dev/null 2>&1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$ROOT/var/lib/slinit/catalog/catalog.compiled" ]; then
    echo "OK: --update-catalog wrote compiled cache"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: compiled cache missing after --update-catalog"
fi

# After update, --list-catalog still finds the ID (via cache).
_out=$(slinit-journalctl --root="$ROOT" --list-catalog 2>&1)
assert_contains "$_out" "deadbeefcafedeadbeefcafedeadbe01" "cached ID still lookupable"

test_summary
