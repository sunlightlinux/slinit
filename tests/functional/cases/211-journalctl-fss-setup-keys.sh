#!/bin/sh
# 211-journalctl-fss-setup-keys — v2.1.8/v2.1.9: --setup-keys
# mints a fresh FSS key and refuses to overwrite an existing key
# without --force (v2.1.9 safety gate).

wait_for_service "boot" "STARTED" 15

KEY=/tmp/fss-key
rm -f "$KEY"

# Fresh mint succeeds and writes the key.
_out=$(slinit-journalctl --setup-keys --fss-key="$KEY" --interval=15m 2>&1)
assert_contains "$_out" "FSS key saved to $KEY" "--setup-keys succeeds"
assert_contains "$_out" "Verification key" "verification token printed"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$KEY" ]; then
    echo "OK: key file written"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: key file not created"
fi

# Second run without --force refuses.
_out=$(slinit-journalctl --setup-keys --fss-key="$KEY" --interval=15m 2>&1)
assert_contains "$_out" "already exists" "second run refuses"
assert_contains "$_out" "--force" "refusal mentions --force"

# --force overwrites; new seed differs from old.
_seed_before=$(grep -oE '"seed": *"[^"]+"' "$KEY")
slinit-journalctl --setup-keys --fss-key="$KEY" --interval=15m --force >/dev/null 2>&1
_seed_after=$(grep -oE '"seed": *"[^"]+"' "$KEY")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_seed_before" != "$_seed_after" ] && [ -n "$_seed_before" ]; then
    echo "OK: --force rotated the seed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: seed did not rotate under --force"
fi

test_summary
