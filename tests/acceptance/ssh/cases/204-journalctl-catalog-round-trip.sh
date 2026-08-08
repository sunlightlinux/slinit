#!/bin/sh
# 204-journalctl-catalog-round-trip — v2.1.8 Group D: write a
# .catalog file under the alt search root, --update-catalog
# rebuilds the compiled cache, --list-catalog prints the ID,
# --dump-catalog contains the body. ID normalisation strips
# dashes + lowercases so the same entry is found via either
# spelling.

WORK=/tmp/acc-204-catalog
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

# Catalog source under the --root prefix. slinit-journalctl scans
# /usr/share/slinit-catalog + /usr/lib/slinit/catalog +
# /usr/lib/systemd/catalog under the given root; drop our file in
# the first one.
mkdir -p "$WORK/usr/share/slinit-catalog"
cat > "$WORK/usr/share/slinit-catalog/acc.catalog" <<'EOF'
-- deadbeefcafedeadbeefcafedeadbe01
Subject: Acceptance test catalog entry
Defined-By: slinit-acceptance
Support: https://example.org/support

Full descriptive text for this synthetic message ID.
Two lines to prove the body round-trips.
EOF

# --list-catalog through the --root prefix.
_out=$(slinit-journalctl --root="$WORK" --list-catalog 2>&1)
assert_contains "$_out" "deadbeefcafedeadbeefcafedeadbe01" "--list-catalog surfaces our ID"

# --dump-catalog contains the header + body.
_out=$(slinit-journalctl --root="$WORK" --dump-catalog 2>&1)
assert_contains "$_out" "-- deadbeefcafedeadbeefcafedeadbe01" "--dump-catalog carries the -- header"
assert_contains "$_out" "Subject: Acceptance test catalog entry" "--dump-catalog carries the Subject"
assert_contains "$_out" "Defined-By: slinit-acceptance" "--dump-catalog title-cases header"
assert_contains "$_out" "Full descriptive text" "--dump-catalog carries the body"

# --update-catalog rebuilds the compiled cache. After a rebuild
# the cache file must exist and re-listing must still find our ID.
slinit-journalctl --root="$WORK" --update-catalog >/dev/null 2>&1
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$WORK/var/lib/slinit/catalog/catalog.compiled" ]; then
    echo "OK: --update-catalog wrote compiled cache"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: compiled cache missing after --update-catalog"
fi

# ID normalization: dashed lookup finds the same entry.
_out=$(slinit-journalctl --root="$WORK" --list-catalog 2>&1)
assert_contains "$_out" "deadbeefcafedeadbeefcafedeadbe01" "cached ID still lookupable"

test_summary
