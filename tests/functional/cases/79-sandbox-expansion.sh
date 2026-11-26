#!/bin/sh
# Test: systemd-style filesystem sandbox expansion (#3b).
# Validates: protect-home=tmpfs gives the service a fresh /home;
# inaccessible-paths hides /opt; temporary-filesystem mounts a tmpfs at
# the chosen path; bind-paths makes a host directory visible.
#
# sandbox-prep drops a marker file into /opt before the service starts.
# The service must not see it and the host must. /opt used to be empty
# in the guest, so "the service sees nothing in /opt" held with or
# without inaccessible-paths.

wait_for_service "sandbox-ext-svc" "STARTED" 15

# The body runs after STARTED for a type=process service.
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -s /var/tmp/sandbox-ext-rw/probe-result ] && break
    sleep 0.5
done

# The bind-path makes /var/tmp/sandbox-ext-rw visible to the host, so we
# can read the probe result the service wrote there.
[ -f /var/tmp/sandbox-ext-rw/probe-result ] && rw=yes || rw=no
assert_eq "$rw" "yes" "bind-paths visible to host"

result=$(cat /var/tmp/sandbox-ext-rw/probe-result 2>/dev/null)

# Pull out each field. Using `expr` keeps the test POSIX-portable.
opt=$(echo "$result" | sed -n 's/.*opt=\([^ ]*\).*/\1/p')
home=$(echo "$result" | sed -n 's/.* home=\([^ ]*\).*/\1/p')
hempty=$(echo "$result" | sed -n 's/.*hempty=\([^ ]*\).*/\1/p')
scratch=$(echo "$result" | sed -n 's/.*scratch=\([^ ]*\).*/\1/p')

[ -f /opt/sandbox-ext-marker ] && host_opt=present || host_opt=absent
assert_eq "$host_opt" "present" "the /opt marker exists on the host (the probe has something to hide)"
assert_eq "$opt" "hidden" "inaccessible-paths hides /opt content"
assert_eq "$home" "blocked" "protect-home=tmpfs gives a read-only /home (as systemd does)"
assert_eq "$hempty" "yes" "protect-home=tmpfs hides the host's /home contents"
assert_eq "$scratch" "writable" "temporary-filesystem mounts writable tmpfs"

# The home probe must NOT leak to the host.
[ -f /home/home-probe ] && leak=present || leak=absent
assert_eq "$leak" "absent" "protect-home tmpfs does not leak to host /home"

test_summary
