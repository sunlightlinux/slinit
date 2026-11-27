#!/bin/sh
# Test: condition-security = measured-os (systemd v261 addition).
# Since 4.1 it is systemd's efi_measured_os: the kernel argument
# systemd.tpm2_measured_os=, else a measured UKI (StubPcrKernelImage
# EFI variable plus a non-empty TPM log). The test VM is a plain QEMU
# without a TPM device or a UKI, so the condition MUST fail — service
# reaches STARTED (silent skip) with no PID.
#
# The positive path (TPM present, PCRs populated) would need
# `-device tpm-tis,tpmdev=…` and swtpm running alongside QEMU. Out
# of scope for the default functional lane; left as a follow-up.

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -e /sys/kernel/security/tpm0/binary_bios_measurements ]; then
    _sz=$(stat -c %s /sys/kernel/security/tpm0/binary_bios_measurements 2>/dev/null)
    if [ -n "$_sz" ] && [ "$_sz" -gt 0 ]; then
        skip_case "TPM measurement log unexpectedly present (size=$_sz); positive path not covered by this test"
    fi
fi
echo "OK: no TPM event log present (expected for a bare QEMU VM)"

wait_for_service "measured-svc" "STARTED" 10
assert_service_state "measured-svc" "STARTED" "measured-svc reached STARTED (silent skip)"

_pid=$(slinitctl --system status measured-svc 2>/dev/null | awk '/PID:/ { print $2; exit }')

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ -z "$_pid" ] || [ "$_pid" = "0" ] || [ "$_pid" = "-" ]; then
    echo "OK: measured-svc skipped — no PID (condition failed as expected)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: measured-svc has PID=$_pid — condition-security=measured-os should have failed on a TPM-less VM"
fi

test_summary
