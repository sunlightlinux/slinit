#!/bin/sh
# 205-journalctl-setup-keys-force — v2.1.8/v2.1.9: --setup-keys
# mints an FSS sealing key, writes to --fss-key path, prints
# verification token. Second run refuses to overwrite without
# --force (v2.1.9 safety gate) since a fresh key invalidates
# every TAG chain sealed with the old key.

KEY=/tmp/acc-205-key
cleanup() { rm -f "$KEY"; }
trap cleanup EXIT INT TERM

# --- fresh run succeeds ------------------------------------------------
rm -f "$KEY"
_out=$(slinit-journalctl --setup-keys --fss-key="$KEY" --interval=15m 2>&1)
assert_contains "$_out" "FSS key saved to $KEY" "fresh --setup-keys succeeds"
assert_contains "$_out" "Verification key" "verification token banner present"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$KEY" ]; then
    echo "OK: key file written"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: key file $KEY not created"
fi

# --- second run without --force refuses --------------------------------
_out=$(slinit-journalctl --setup-keys --fss-key="$KEY" --interval=15m 2>&1)
assert_contains "$_out" "already exists" "second run mentions existing file"
assert_contains "$_out" "--force" "refusal message names --force"

# --- --force allows overwrite -----------------------------------------
# Snapshot the current seed, overwrite with --force, verify a
# NEW seed landed (proves overwrite happened, not just re-parse).
_seed_before=$(grep -oE '"seed": *"[^"]+"' "$KEY")
_out=$(slinit-journalctl --setup-keys --fss-key="$KEY" --interval=15m --force 2>&1)
assert_contains "$_out" "FSS key saved to $KEY" "--force succeeds"
_seed_after=$(grep -oE '"seed": *"[^"]+"' "$KEY")
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_seed_before" != "$_seed_after" ] && [ -n "$_seed_before" ] && [ -n "$_seed_after" ]; then
    echo "OK: --force overwrote with a fresh seed"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: seed did not rotate under --force"
fi

test_summary
