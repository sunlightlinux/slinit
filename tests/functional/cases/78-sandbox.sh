#!/bin/sh
# Test: systemd-style filesystem sandbox (#3a MVP).
# Validates: private-tmp gives the service a /tmp invisible from the
# host; read-only-paths blocks writes to /etc; read-write-paths punches
# a host-visible writable hole through the sandbox.

wait_for_service "sandbox-svc" "STARTED" 15

# /etc/sandbox-rw is a read-write-path punched through the read-only
# /etc, so the file should be visible on the host. It used to live under
# /run, which nothing made read-only: the write landed there with or
# without read-write-paths. (Not /var/tmp: private-tmp replaces it.)
[ -f /etc/sandbox-rw/marker ] && rw=yes || rw=no
assert_eq "$rw" "yes" "read-write-path visible to host"

# The probe's result tells us whether /etc was blocked.
result=$(cat /etc/sandbox-rw/result 2>/dev/null)
assert_eq "$result" "etc-protected" "read-only-paths blocks /etc writes"

# Private-tmp: the file the service dropped in its private /tmp must NOT
# be visible on the host (which has its own /tmp).
[ -f /tmp/sandbox-private ] && pt=leaked || pt=isolated
assert_eq "$pt" "isolated" "private-tmp isolates /tmp from host"

# No escape file should exist on the host's /etc.
[ -f /etc/sandbox-escape ] && esc=present || esc=absent
assert_eq "$esc" "absent" "/etc/sandbox-escape did not reach the host"

test_summary
