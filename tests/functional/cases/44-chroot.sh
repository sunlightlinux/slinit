#!/bin/sh
# Test: chroot isolates service to a root directory.
# Validates: chroot setting on ProcessService.
# Note: VM runs as root, so chroot works without user namespaces.

# Set up a minimal chroot environment
mkdir -p /tmp/chroot-root/bin /tmp/chroot-root/tmp /tmp/chroot-root/proc /tmp/chroot-root/dev
cp /bin/busybox /tmp/chroot-root/bin/busybox 2>/dev/null || cp /bin/sh /tmp/chroot-root/bin/sh
# Create sh symlink for busybox-based systems
ln -sf busybox /tmp/chroot-root/bin/sh 2>/dev/null
ln -sf busybox /tmp/chroot-root/bin/ls 2>/dev/null
ln -sf busybox /tmp/chroot-root/bin/pwd 2>/dev/null
ln -sf busybox /tmp/chroot-root/bin/sleep 2>/dev/null
ln -sf busybox /tmp/chroot-root/bin/cat 2>/dev/null

# Bind-mount /tmp so we can read the result from host
mount --bind /tmp/chroot-root/tmp /tmp/chroot-root/tmp 2>/dev/null || true

wait_for_service "chroot-svc" "STARTED" 10

# Give service time to write result
sleep 2

# The service writes to /tmp/chroot-result inside the chroot,
# which maps to /tmp/chroot-root/tmp/chroot-result on the host
result=$(cat /tmp/chroot-root/tmp/chroot-result 2>/dev/null)
assert_contains "$result" "/" "chroot process produced output"

assert_service_state "chroot-svc" "STARTED" "chroot-svc is STARTED"

test_summary
