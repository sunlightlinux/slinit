#!/bin/sh
# 206-journalctl-vacuum — v2.1.10 --vacuum-{size,files,time}
# runs journald.Vacuum directly on the target dir, excluding the
# active current-day file so a live daemon never sees its writer
# disappear. Missing directory is a benign no-op.

# Missing dir → no-op with informational line, NOT an error.
_out=$(slinit-journalctl --vacuum-size=1M --directory=/nonexistent-vac-$$ 2>&1)
assert_contains "$_out" "nothing to vacuum" "missing dir → benign no-op"

# Seed a work dir with 3 pre-rotated + 1 "current" file, prune
# to keep only 2 files (should keep the 2 most recent rotated).
WORK=/tmp/acc-206-vacuum
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM
mkdir -p "$WORK"

# Current-day file — must survive vacuum (excluded).
_today=$(date -u +%Y-%m-%d)
touch "$WORK/${_today}.jsonl"

# Rotated archives (older). Different mtimes so vacuum's oldest-
# first pruning is deterministic.
touch -t 202601010000 "$WORK/2025-01-01.oldest.jsonl"
touch -t 202606010000 "$WORK/2025-06-01.middle.jsonl"
touch -t 202610010000 "$WORK/2025-10-01.newest.jsonl"

_before=$(ls "$WORK" | wc -l)
assert_eq "$_before" "4" "seeded 4 files before vacuum"

# Keep 2 → drops the oldest rotated. Current file stays regardless.
slinit-journalctl --vacuum-files=2 --directory="$WORK" >/dev/null 2>&1

_after=$(ls "$WORK" | wc -l)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_after" -le 3 ]; then
    echo "OK: vacuum reduced file count from 4 to $_after"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: vacuum did not prune ($_after files remain)"
fi

# Current file must always survive.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -f "$WORK/${_today}.jsonl" ]; then
    echo "OK: current-day file preserved through vacuum"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: current-day file was incorrectly pruned"
fi

test_summary
