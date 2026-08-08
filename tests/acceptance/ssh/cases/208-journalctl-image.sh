#!/bin/sh
# 208-journalctl-image — v2.1.12 --image: pkg/dissect shells out
# to losetup + mount + lsblk, probes each partition for a slinit-
# journal directory, and re-enters the standard --directory scan.
# Defer'd detach always runs — even on error — so a broken image
# never leaks a loop device.

IMG=/tmp/acc-208-img.raw
cleanup() {
    # Best-effort cleanup for a case that crashed mid-attach.
    for lo in $(losetup -a 2>/dev/null | awk -F: '/acc-208-img/ {print $1}'); do
        umount "$lo" 2>/dev/null
        losetup --detach "$lo" 2>/dev/null
    done
    rm -f "$IMG"
}
trap cleanup EXIT INT TERM

# Preconditions: losetup + mkfs.ext4 must exist. Skip cleanly if
# util-linux isn't fully installed rather than fail spuriously.
_TESTS_RUN=$((_TESTS_RUN + 1))
if ! command -v losetup >/dev/null || ! command -v mkfs.ext4 >/dev/null; then
    echo "SKIP: losetup or mkfs.ext4 missing — image dissect needs util-linux"
    test_summary
    return
fi
echo "OK: prereqs (losetup, mkfs.ext4) present"

# 20MB raw ext4 image with a synthetic journal event inside.
truncate -s 20M "$IMG"
mkfs.ext4 -q "$IMG"
MNT=/tmp/acc-208-mnt
mkdir -p "$MNT"
mount -o loop "$IMG" "$MNT"
mkdir -p "$MNT/var/log/slinit-journal"
_today=$(date -u +%Y-%m-%d)
echo '{"ts":42,"msg":"acc-208 synthetic event","prio":6,"unit":"acc-208-svc"}' \
    > "$MNT/var/log/slinit-journal/${_today}.jsonl"
umount "$MNT"
rmdir "$MNT"

# The actual dissect query.
_out=$(slinit-journalctl --image="$IMG" -o cat 2>&1)
assert_contains "$_out" "acc-208 synthetic event" "--image mounted + rendered event"

# Clean detach: after slinit-journalctl exits, no loop device
# should reference our image path.
_TESTS_RUN=$((_TESTS_RUN + 1))
if losetup -a 2>/dev/null | grep -q "$IMG"; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: loop device for $IMG still attached after exit"
else
    echo "OK: loop device detached cleanly"
fi

# --image-policy=strict on a plain (non-encrypted) fs image must
# succeed — there's nothing to refuse.
_out=$(slinit-journalctl --image="$IMG" --image-policy=strict -o cat 2>&1)
assert_contains "$_out" "acc-208 synthetic event" "--image-policy=strict OK on plain fs"

# Nonexistent path returns a clean stat error, not a crash.
_out=$(slinit-journalctl --image=/nonexistent-$$.img 2>&1)
assert_contains "$_out" "no such file" "--image on missing path errors cleanly"

test_summary
