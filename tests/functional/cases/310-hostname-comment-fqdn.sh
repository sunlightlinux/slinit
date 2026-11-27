#!/bin/sh
# Test: PID 1 sets the kernel hostname from /etc/hostname as systemd reads it.
#
# .d/rootfs seeds /etc/hostname with a comment, a blank line and an FQDN
# written with the DNS trailing dot. Up to v4.0.3 PID 1 handed the whole
# file, trimmed, to sethostname: the comment and the newline became part
# of the hostname, and the trailing dot stayed (0667d6f). systemd's
# read_etc_hostname takes the first non-comment line and its
# hostname_cleanup drops the trailing dot. (296 covers a name that is
# invalid; this one is valid once read correctly.)

wait_for_service boot STARTED 10
_h=$(hostname)
echo "hostname: '$_h'"
assert_eq "$_h" "node-310.example.com" "the hostname is the first name line, without the trailing dot"
_k=$(cat /proc/sys/kernel/hostname)
assert_eq "$_k" "node-310.example.com" "the kernel's UTS name agrees"

test_summary
