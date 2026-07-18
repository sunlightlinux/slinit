#!/bin/sh
# Test: condition-security = measured-uki on a stock QEMU (no
# StubPcrKernelImage EFI var, no TPM). The predicate must skip
# the command silently — the service reaches STARTED but no
# process runs.
rm -f /tmp/measured-uki-ran
wait_for_service "measured-uki-svc" "STARTED" 10
assert_service_state "measured-uki-svc" "STARTED" "svc reached STARTED"

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -e /tmp/measured-uki-ran ]; then
    echo "OK: command skipped (condition false on QEMU without UKI)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: measured-uki predicate should have skipped, marker exists"
fi

test_summary
