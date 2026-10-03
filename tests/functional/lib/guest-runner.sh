#!/bin/sh
# guest-runner.sh - Runs inside the QEMU guest as PID 1's test harness.
#
# slinit boots normally, then this script (injected as a service) runs
# the test case, writes results to virtio-serial, and triggers shutdown.
set -e

RESULT_DEV="/dev/vport0p1"
TEST_SCRIPT="/test/current-test.sh"

# Source assertion helpers
. /test/assert.sh

# Wait for slinit control socket to be ready
_wait=0
while [ ! -S /run/slinit.socket ] && [ "$_wait" -lt 15 ]; do
    sleep 1
    _wait=$((_wait + 1))
done

if [ ! -S /run/slinit.socket ]; then
    echo "FATAL: slinit control socket not available after 15s" > "${RESULT_DEV}"
    timeout 10 slinitctl --system shutdown poweroff 2>/dev/null || true
    exit 1
fi

# Small delay to let boot services settle
sleep 1

# Run the test script, capturing output.
# Capture exit code without letting set -e kill us.
set +e
# Streamed to the console as it happens, as well as collected.
#
# The result channel is only written after the whole script returns, so a
# script that hangs anywhere delivered ZERO bytes — which is why three
# "no result received" failures (181 twice, 72 once) each looked identical
# and said nothing. The console log is captured as an artifact and survives
# a hang, so sending the output there too means the next hang arrives with
# every assertion up to that point and the line it died on.
#
# The status goes through a FILE and not through `$?`, which is the part
# that matters. `$?` after a pipeline is the status of its LAST command —
# `tee`, which always succeeds — so the obvious `... | tee /dev/console`
# followed by `_rc=$?` reports every case as PASS no matter what it
# asserted. That is not hypothetical: it is what this file did between the
# console-streaming change and this one, and `fix: guest-runner exit code
# capture masked test failures` (e1673d4, April) had already fixed the
# same bug in its `|| true` form. `pipefail` would do here, but it is not
# POSIX and the guest shell is whatever the image ships; a file is.
_RC_FILE="/run/slinit-test-rc"
rm -f "${_RC_FILE}"
_output=$( { (. "${TEST_SCRIPT}"); echo "$?" > "${_RC_FILE}"; } 2>&1 | tee /dev/console )
_rc=$(cat "${_RC_FILE}" 2>/dev/null)
# No file means the group never reached its last command — treat an
# unknown status as a failure rather than as a pass.
[ -n "${_rc}" ] || _rc=1
set -e

# Write results to virtio-serial port
{
    echo "${_output}"
    case "$_rc" in
        0)  echo "TEST_RESULT:PASS" ;;
        # 77 is "this case cannot run here", the convention the container
        # suite already uses. It is deliberately not 0: a case that skips
        # must not be counted as a case that verified something.
        77) echo "TEST_RESULT:SKIP" ;;
        *)  echo "TEST_RESULT:FAIL (exit code $_rc)" ;;
    esac
} > "${RESULT_DEV}"

# Trigger clean shutdown
sleep 1
# Bounded too: a poweroff that never returns leaves the VM up until the
# harness kills it. The result is already on the wire by this point, so the
# case still reports either way.
timeout 10 slinitctl --system shutdown poweroff 2>/dev/null || true
