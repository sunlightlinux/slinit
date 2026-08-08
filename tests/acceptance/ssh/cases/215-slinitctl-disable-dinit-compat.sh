#!/bin/sh
# 215-slinitctl-disable-dinit-compat — v2.1.0 landed the dual-wire
# disable: slinit-native atomic V7 opcode (CmdDisableServiceV7=62)
# is the default; --dinit-compat routes through the older
# CmdRmDepV7 (opcode 30) so slinitctl can talk to a real dinit
# daemon too. Both paths must be accessible via the CLI.

SVC="acceptance-test-dinit-disable"
# slinit enable creates a symlink at /etc/slinit.d/waits-for.d/<svc>
# (not boot.d — that's a different mechanism). Both disable paths
# must remove the symlink to be considered successful.
LINK="/etc/slinit.d/waits-for.d/${SVC}"
cleanup() { svc_remove "$SVC"; rm -f "$LINK"; }
trap cleanup EXIT INT TERM

svc_deploy "$SVC" <<'EOF'
type = process
command = /bin/sh -c 'exec sleep 600'
restart = false
EOF

# --- default (atomic V7) path -----------------------------------------
_out=$(slinitctl --system enable "$SVC" 2>&1)
assert_contains "$_out" "enabled" "enable prints confirmation"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -L "$LINK" ]; then
    echo "OK: enable created waits-for.d symlink"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: enable did not create $LINK"
fi

_out=$(slinitctl --system disable "$SVC" 2>&1)
assert_contains "$_out" "disabled" "default disable prints confirmation"
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -L "$LINK" ]; then
    echo "OK: default disable removed symlink (atomic V7 path)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: symlink still present after default disable"
fi

# --- dinit-compat path -------------------------------------------------
slinitctl --system enable "$SVC" >/dev/null 2>&1
_out=$(slinitctl --system --dinit-compat disable "$SVC" 2>&1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -L "$LINK" ]; then
    echo "OK: --dinit-compat disable removed symlink (client-side + rm-dep opcode 30)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: symlink still present after --dinit-compat disable"
fi

test_summary
