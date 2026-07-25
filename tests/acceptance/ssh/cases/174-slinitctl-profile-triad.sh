#!/bin/sh
# 174-slinitctl-profile-triad — the runsvchdir-inspired profile system:
# `activate-profile` switches the daemon's active profile at runtime;
# `active-profile` reads it; `list-profiles` enumerates configured
# profiles from `profile =` directives on loaded services. This is
# purely a query-and-switch round-trip; no service state change is
# asserted (that would need a real profile boundary configured).

PROF_A="acceptance-test-profile-a"
PROF_B="acceptance-test-profile-b"

# Remember the initial profile so we can restore it. Empty string
# is the valid "no active profile" state.
_orig=$(slinitctl --system active-profile 2>/dev/null | tr -d '\n')

cleanup() {
    slinitctl --system activate-profile "${_orig:--}" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

# --- active-profile is queryable ---------------------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
_ap=$(slinitctl --system active-profile 2>&1)
if [ $? -eq 0 ]; then
    echo "OK: slinitctl active-profile returned successfully ('$_ap')"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinitctl active-profile failed: $_ap"
fi

# --- list-profiles is queryable (may be empty on a fresh system) ------
_TESTS_RUN=$((_TESTS_RUN + 1))
_lp=$(slinitctl --system list-profiles 2>&1)
if [ $? -eq 0 ]; then
    echo "OK: slinitctl list-profiles returned successfully"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinitctl list-profiles failed: $_lp"
fi

# --- activate-profile switches the value -------------------------------
slinitctl --system activate-profile "$PROF_A" >/dev/null 2>&1
sleep 1
_now=$(slinitctl --system active-profile 2>/dev/null | tr -d '\n')
assert_eq "$_now" "$PROF_A" "activate-profile $PROF_A → active-profile reports $PROF_A"

# --- activate-profile switches again -----------------------------------
slinitctl --system activate-profile "$PROF_B" >/dev/null 2>&1
sleep 1
_now=$(slinitctl --system active-profile 2>/dev/null | tr -d '\n')
assert_eq "$_now" "$PROF_B" "activate-profile $PROF_B → active-profile reports $PROF_B"

test_summary
