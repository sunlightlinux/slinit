#!/bin/sh
# Test: systemd-style Restrict*/Protect* hardening cluster (#7 v1).
# Validates:
#   - protect-control-groups remounts /sys/fs/cgroup read-only;
#   - protect-clock blocks clock-setting syscalls (date -s fails);
#   - protect-hostname blocks hostname change (hostname cmd fails).

wait_for_service "hardened-svc" "STARTED" 15

[ -f /var/tmp/hardening-out/result ] && got=yes || got=no
assert_eq "$got" "yes" "probe wrote its result file"

result=$(cat /var/tmp/hardening-out/result 2>/dev/null)
cgroup=$(echo "$result" | sed -n 's/.*cgroup=\([^ ]*\).*/\1/p')
clock=$(echo "$result" | sed -n 's/.*clock=\([^ ]*\).*/\1/p')
host=$(echo "$result" | sed -n 's/.*host=\([^ ]*\).*/\1/p')

assert_eq "$cgroup" "protected" "protect-control-groups makes /sys/fs/cgroup read-only"
assert_eq "$clock" "protected" "protect-clock blocks date -s"
assert_eq "$host" "protected" "protect-hostname blocks hostname change"

# The hostname must not actually have been changed on the host.
real=$(hostname 2>/dev/null)
[ "$real" = "slinit-pwn" ] && leak=yes || leak=no
assert_eq "$leak" "no" "hostname change did not leak to host"

test_summary
