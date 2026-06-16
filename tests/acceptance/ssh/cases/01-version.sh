#!/bin/sh
# 01-version — verify PID 1 is slinit and slinitctl reports a sensible version.

# PID 1 comm: 'slinit' (when launched as /usr/bin/slinit) or 'init' (when
# launched via /sbin/init symlink). Either is acceptable.
_comm="$(cat /proc/1/comm 2>/dev/null)"
case "$_comm" in
    slinit|init)
        _TESTS_RUN=$((_TESTS_RUN + 1))
        echo "OK: PID 1 comm is '$_comm'"
        ;;
    *)
        _TESTS_RUN=$((_TESTS_RUN + 1))
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: PID 1 comm is '$_comm' (expected slinit or init)"
        ;;
esac

# slinitctl --version must print a v-prefixed semver.
_ver="$(slinitctl --version 2>&1)"
assert_contains "$_ver" "slinitctl version v" "version banner"

# The binary running as PID 1 should match the slinitctl version (no skew
# between an upgraded daemon and a stale client). slinit's own --version line
# may carry a trailing ' (platform: …)' from the platform probe; strip it
# before comparing.
_pid1_exe=$(readlink /proc/1/exe 2>/dev/null || echo "")
if [ -n "$_pid1_exe" ] && [ -x "$_pid1_exe" ]; then
    _pid1_ver=$("$_pid1_exe" --version 2>&1 | head -1)
    _client_ver=$(echo "$_ver" | head -1)
    # Extract just the vX.Y.Z token from each banner.
    _pid1_token=$(echo "$_pid1_ver"   | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1)
    _client_token=$(echo "$_client_ver" | grep -oE 'v[0-9]+\.[0-9]+\.[0-9]+' | head -1)
    assert_eq "$_pid1_token" "$_client_token" "PID 1 / slinitctl version match"
else
    echo "OK: skipping PID1/client version match (no /proc/1/exe readlink)"
fi

test_summary
