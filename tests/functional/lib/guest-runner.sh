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
    slinitctl --system shutdown poweroff 2>/dev/null || true
    exit 1
fi

# Small delay to let boot services settle
sleep 1

# Run the test script, capturing output
_output=$( (. "${TEST_SCRIPT}") 2>&1 ) || true
_rc=$?

# Write results to virtio-serial port
{
    echo "${_output}"
    if [ "$_rc" -eq 0 ]; then
        echo "TEST_RESULT:PASS"
    else
        echo "TEST_RESULT:FAIL (exit code $_rc)"
    fi
} > "${RESULT_DEV}"

# Trigger clean shutdown
sleep 1
slinitctl --system shutdown poweroff 2>/dev/null || true
