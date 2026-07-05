#!/bin/sh
# 54-platform-detect — virtualization detection.
#
# `slinitctl platform` shells out to pkg/platform.Detect() and prints:
#
#   Platform: kvm
#   Platform: qemu
#   Platform: vmware
#   Platform: bare-metal      (when Detect() == platform.None)
#   ...
#
# The acceptance VM is a QEMU/KVM guest, so we expect `kvm`. We don't
# hard-code that — we cross-check against the kernel's own evidence
# (`/sys/class/dmi/id/sys_vendor`, `/sys/class/dmi/id/product_name`,
# and the kernel's clocksource) so the test stays valid on bare metal
# or on non-KVM hypervisors.

_out=$(slinitctl --system platform 2>&1)

# --- Shape: starts with "Platform: " -------------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    "Platform: "*)
        echo "OK: output has 'Platform: ' prefix ($_out)"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: missing 'Platform: ' prefix: '$_out'"
        test_summary
        exit 1
        ;;
esac

# Extract the detected platform token.
_detected=$(echo "$_out" | sed 's/^Platform: //; s/[[:space:]]*$//')

# --- Token shape: lower-case, no spaces ----------------------------
# Detect() returns enum strings like "kvm", "qemu", "vmware", "hyperv",
# "virtualbox", "bochs", "xen", or "bare-metal". All lowercase, no
# embedded whitespace.
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_detected" in
    *' '*|*[A-Z]*|"")
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: token '$_detected' has whitespace, uppercase, or is empty"
        ;;
    *)
        echo "OK: token '$_detected' is well-formed"
        ;;
esac

# --- Cross-check against kernel-provided evidence ------------------
# Independently derive what the platform *should* be, then assert the
# match. This catches both regressions in detect.go and any drift in
# the VM's hardware emulation.
expected=""

# Strongest signal: kvm-clock listed as an available clocksource. The
# kernel only registers it under KVM acceleration — pure-TCG QEMU never
# exposes kvm-clock. We deliberately check `available_clocksource` (not
# `current_clocksource`) so the test still recognizes KVM when the boot
# cmdline pins a different source (clocksource=tsc / tsc=reliable).
_clock=$(cat /sys/devices/system/clocksource/clocksource0/current_clocksource 2>/dev/null)
_avail=$(cat /sys/devices/system/clocksource/clocksource0/available_clocksource 2>/dev/null)
case " $_avail " in
    *' kvm-clock '*) expected="kvm" ;;
esac

# DMI sys_vendor / product_name lookups.
_sys_vendor=$(tr -d '\0\n' < /sys/class/dmi/id/sys_vendor 2>/dev/null)
_product=$(tr -d '\0\n' < /sys/class/dmi/id/product_name 2>/dev/null)

if [ -z "$expected" ]; then
    case "$_sys_vendor" in
        QEMU|qemu)
            # QEMU vendor with no kvm-clock — pure-TCG.
            expected="qemu"
            ;;
        VMware*|VMware,*)
            expected="vmware"
            ;;
        "Microsoft Corporation")
            case "$_product" in
                *"Virtual Machine"*) expected="hyperv" ;;
            esac
            ;;
        "innotek GmbH")
            expected="virtualbox"
            ;;
        Bochs)
            expected="bochs"
            ;;
        Xen)
            expected="xen"
            ;;
    esac
fi

# Bare-metal fallback: nothing virtualization-flavoured in DMI and no
# kvm-clock — accept whatever slinitctl says as long as it's "bare-metal".
if [ -z "$expected" ]; then
    expected="bare-metal"
fi

assert_eq "$_detected" "$expected" "platform matches kernel-derived expectation"

# --- Echo the evidence for a forensic trail ------------------------
echo "  evidence: clocksource=$_clock sys_vendor='$_sys_vendor' product_name='$_product'"

test_summary
