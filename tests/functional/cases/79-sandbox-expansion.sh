#!/bin/sh
# Test: systemd-style filesystem sandbox expansion (#3b).
# Validates: protect-home=tmpfs gives the service a fresh /home;
# inaccessible-paths hides /opt; temporary-filesystem mounts a tmpfs at
# the chosen path; bind-paths makes a host directory visible.

wait_for_service "sandbox-ext-svc" "STARTED" 15

# The bind-path makes /var/tmp/sandbox-ext-rw visible to the host, so we
# can read the probe result the service wrote there.
[ -f /var/tmp/sandbox-ext-rw/probe-result ] && rw=yes || rw=no
assert_eq "$rw" "yes" "bind-paths visible to host"

result=$(cat /var/tmp/sandbox-ext-rw/probe-result 2>/dev/null)

# Pull out each field. Using `expr` keeps the test POSIX-portable.
opt=$(echo "$result" | sed -n 's/.*opt=\([^ ]*\).*/\1/p')
home=$(echo "$result" | sed -n 's/.*home=\([^ ]*\).*/\1/p')
scratch=$(echo "$result" | sed -n 's/.*scratch=\([^ ]*\).*/\1/p')

assert_eq "$opt" "hidden" "inaccessible-paths hides /opt content"
assert_eq "$home" "writable" "protect-home=tmpfs gives writable empty /home"
assert_eq "$scratch" "writable" "temporary-filesystem mounts writable tmpfs"

# The home probe must NOT leak to the host.
[ -f /home/home-probe ] && leak=present || leak=absent
assert_eq "$leak" "absent" "protect-home tmpfs does not leak to host /home"

test_summary
