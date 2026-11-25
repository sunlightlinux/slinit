#!/bin/sh
# 214-slinitctl-disable-dual-wire — v2.1.0 6b5f58d: `slinitctl
# disable` uses the atomic V7 opcode (CmdDisableServiceV7=62)
# by default; `--dinit-compat` routes through the older
# CmdRmDepV7 (opcode 30) so the CLI can talk to a real dinit
# daemon too. Both paths must roundtrip through enable →
# disable → symlink presence checks.

# boot deliberately does NOT list dwsvc: an edge already there makes
# enable a no-op that writes no symlink, which is how this case used to
# pass without ever seeing one. enable defaults its "from" to boot, and
# boot declares no waits-for.d:, so the link lands in the fallback
# <service dir>/waits-for.d/ — slinit creates that directory itself.
LINK="/etc/slinit.d/waits-for.d/dwsvc"
rm -f "$LINK"

assert_service_state "dwsvc" "STOPPED" "dwsvc down before enable (nothing pulls it in)"

check_link_present() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ -L "$LINK" ] && [ "$(readlink "$LINK")" = "../dwsvc" ]; then
        echo "OK: $1: $LINK -> ../dwsvc"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $1: no $LINK -> ../dwsvc symlink"
        ls -l /etc/slinit.d/waits-for.d/ 2>&1
    fi
}

check_link_absent() {
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ ! -e "$LINK" ] && [ ! -L "$LINK" ]; then
        echo "OK: $1: $LINK removed"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: $1: $LINK still present"
    fi
}

# --- default (atomic V7) path ---
_out=$(slinitctl --system enable dwsvc 2>&1)
assert_contains "$_out" "enabled" "enable prints confirmation"
check_link_present "enable persisted the waits-for.d symlink"
# enable starts the target through the new edge.
wait_for_service "dwsvc" "STARTED" 10 || true
assert_service_state "dwsvc" "STARTED" "enable started dwsvc"

_out=$(slinitctl --system disable dwsvc 2>&1)
assert_contains "$_out" "disabled" "default disable prints confirmation"
check_link_absent "default disable"

# --- --dinit-compat path (client-side removal + rm-dep opcode) ---
slinitctl --system enable dwsvc >/dev/null 2>&1
# Without a link to remove, the compat removal below would pass on nothing.
check_link_present "re-enable persisted the symlink again"
_out=$(slinitctl --system --dinit-compat disable dwsvc 2>&1)
assert_contains "$_out" "disabled" "--dinit-compat disable prints confirmation"
check_link_absent "--dinit-compat disable"

test_summary
