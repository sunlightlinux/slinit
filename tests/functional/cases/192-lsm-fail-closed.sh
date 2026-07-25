#!/bin/sh
# Test: LSM directives fail-closed when the corresponding LSM is
# absent from /sys.
# Validates SECURITY.md's fail-closed contract:
#   selinux-context   requires /sys/fs/selinux
#   smack-process-label requires /sys/fs/smackfs
# Alpine's linux-virt has neither, so both services MUST refuse to
# start — an accidental fail-open would let production traffic run
# without the confinement the operator declared.

# Give the boot graph time to attempt both bring-ups and fail.
sleep 3

# --- SELinux: fail-closed when /sys/fs/selinux absent ---------------
if [ -d /sys/fs/selinux ]; then
    echo "  note: /sys/fs/selinux present on this VM — SELinux fail-closed test skipped"
else
    _state=$(slinitctl --system status selinux-svc 2>/dev/null | awk '/State:/ { print $2; exit }')
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$_state" != "STARTED" ]; then
        echo "OK: selinux-context fail-closed — svc did not start (state: $_state)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: selinux-context did NOT fail-closed — svc reached STARTED"
    fi
fi

# --- SMACK: fail-closed when /sys/fs/smackfs absent -----------------
if [ -d /sys/fs/smackfs ]; then
    echo "  note: /sys/fs/smackfs present on this VM — SMACK fail-closed test skipped"
else
    _state=$(slinitctl --system status smack-svc 2>/dev/null | awk '/State:/ { print $2; exit }')
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ "$_state" != "STARTED" ]; then
        echo "OK: smack-process-label fail-closed — svc did not start (state: $_state)"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: smack-process-label did NOT fail-closed — svc reached STARTED"
    fi
fi

test_summary
