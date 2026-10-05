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
echo "INFO: probe recorded: $result"
cgroup=$(echo "$result" | sed -n 's/.*cgroup=\([^ ]*\).*/\1/p')
clock=$(echo "$result" | sed -n 's/.*clock=\([^ ]*\).*/\1/p')
host=$(echo "$result" | sed -n 's/.*host=\([^ ]*\).*/\1/p')
seccomp=$(echo "$result" | sed -n 's/.*seccomp=\([^ ]*\).*/\1/p')
nnp=$(echo "$result" | sed -n 's/.*nnp=\([^ ]*\).*/\1/p')
cgmount=$(echo "$result" | sed -n 's/.*cgmount=\([^ ]*\).*/\1/p')

# Assert the filter is there BEFORE asserting what it blocks. Without
# this, "clock=writable" could mean either "no filter" or "filter does
# not cover clock_settime", and the two have nothing in common except
# the symptom. /proc/self/status Seccomp: 2 == SECCOMP_MODE_FILTER.
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$seccomp" = "2" ]; then
    echo "OK: the service runs under a seccomp filter (Seccomp: 2)"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no seccomp filter on the service (Seccomp: ${seccomp:-unknown}," \
         "NoNewPrivs: ${nnp:-unknown}) — slinit-runner did not install one," \
         "so the protect-clock/protect-hostname results below say nothing" \
         "about the filter's contents"
fi

# protect-control-groups remounts /sys/fs/cgroup read-only only where it
# IS a mount, so the INFO line above carries the mount's fstype and
# options: a write failing against an already-read-only mount proves
# less than one failing against a writable cgroup2. In this VM the entry
# is `sysfs ro,relatime` and there is no cgroup v2 at all
# (141-psi-pressure skips for that reason). 114-protect-control-groups
# is the case that tests the knob properly, by comparing the service's
# own mountinfo against PID 1's; this one is a composition check.
case "$cgmount" in
    yes:*) assert_eq "$cgroup" "protected" "protect-control-groups makes /sys/fs/cgroup read-only" ;;
    *)     echo "INFO: /sys/fs/cgroup is not a mount here, so cgroup=$cgroup is not attributable" \
                "to protect-control-groups" ;;
esac
assert_eq "$clock" "protected" "protect-clock blocks date -s"
assert_eq "$host" "protected" "protect-hostname blocks hostname change"

# The hostname must not actually have been changed on the host.
real=$(hostname 2>/dev/null)
[ "$real" = "slinit-pwn" ] && leak=yes || leak=no
assert_eq "$leak" "no" "hostname change did not leak to host"

test_summary
