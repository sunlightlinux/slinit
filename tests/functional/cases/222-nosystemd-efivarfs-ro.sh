#!/bin/sh
# nosystemd checklist — systemd#2402.
#
# "Mount efivarfs read-only": systemd mounted efivarfs read-write by
# default, so an `rm -rf /` (or any careless recursive delete) could
# wipe EFI variables and brick the firmware. The famous
# "rm -rf /sys/firmware/efi/efivars bricks your laptop" class.
# https://github.com/systemd/systemd/issues/2402
#
# slinit must not mount efivarfs read-write. On a VM without EFI the
# mount is simply absent, which satisfies the property trivially — the
# test records which case it saw so the result is never ambiguous.

_TESTS_RUN=$((_TESTS_RUN + 1))
if [ ! -d /sys/firmware/efi ]; then
    echo "OK: no EFI firmware in this VM — efivarfs cannot be mounted rw"
    test_summary
    return 0 2>/dev/null || exit 0
fi

mnt=$(awk '$2 == "/sys/firmware/efi/efivars" {print $4}' /proc/self/mounts 2>/dev/null | head -1)
if [ -z "$mnt" ]; then
    echo "OK: efivarfs is not mounted"
    test_summary
    return 0 2>/dev/null || exit 0
fi

case "$mnt" in
    ro|ro,*|*,ro|*,ro,*)
        echo "OK: efivarfs mounted read-only ($mnt)"
        ;;
    *)
        echo "FAIL: efivarfs mounted read-write ($mnt) — systemd#2402 shape"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        ;;
esac

test_summary
