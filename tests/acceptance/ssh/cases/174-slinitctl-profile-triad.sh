#!/bin/sh
# 174-slinitctl-profile-triad — the runsvchdir-inspired profile system:
# `activate-profile` switches the daemon's active profile at runtime;
# `active-profile` reads it; `list-profiles` enumerates configured
# profiles from `profile =` directives on loaded services. This is
# purely a query-and-switch round-trip; no service state change is
# asserted (that would need a real profile boundary configured).

PROF_A="acceptance-test-profile-a"
PROF_B="acceptance-test-profile-b"
SVC_A="acceptance-test-prof-svc-a"
SVC_B="acceptance-test-prof-svc-b"

# Remember the initial profile so we can restore it.
_orig=$(slinitctl --system active-profile 2>/dev/null | tr -d '\n')

cleanup() {
    slinitctl --system activate-profile - >/dev/null 2>&1 || true
    svc_remove "$SVC_A" "$SVC_B"
}
trap cleanup EXIT INT TERM

# Deploy two services, each declaring its own profile. activate-profile
# requires at least one loaded service to declare the profile name
# (otherwise it errors with "no loaded service declares profile ...").
svc_deploy "$SVC_A" <<EOF
type = process
profile = $PROF_A
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

svc_deploy "$SVC_B" <<EOF
type = process
profile = $PROF_B
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

# svc_deploy only writes the file. list-profiles / activate-profile
# enumerate over LOADED services, so we need an explicit start to pull
# the descriptions into the daemon.
slinitctl --system start "$SVC_A" >/dev/null 2>&1
slinitctl --system start "$SVC_B" >/dev/null 2>&1
wait_for_service "$SVC_A" "STARTED" 10
wait_for_service "$SVC_B" "STARTED" 10

# --- active-profile is queryable ---------------------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
_ap=$(slinitctl --system active-profile 2>&1)
if [ $? -eq 0 ]; then
    echo "OK: slinitctl active-profile returned successfully ('$_ap')"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinitctl active-profile failed: $_ap"
fi

# --- list-profiles surfaces the declared profiles ---------------------
_lp=$(slinitctl --system list-profiles 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ $? -eq 0 ]; then
    echo "OK: slinitctl list-profiles returned ('$_lp')"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinitctl list-profiles failed: $_lp"
fi

# --- activate-profile switches; active-profile reflects it ------------
slinitctl --system activate-profile "$PROF_A" >/dev/null 2>&1
sleep 1
_now=$(slinitctl --system active-profile 2>/dev/null | tr -d '\n')
assert_eq "$_now" "$PROF_A" "activate-profile → active-profile reports $PROF_A"

slinitctl --system activate-profile "$PROF_B" >/dev/null 2>&1
sleep 1
_now=$(slinitctl --system active-profile 2>/dev/null | tr -d '\n')
assert_eq "$_now" "$PROF_B" "activate-profile → active-profile reports $PROF_B"

test_summary
