#!/bin/sh
# 02-control-socket — control socket exists, list works, status works.

assert_eq "$(test -S /run/slinit.socket && echo yes || echo no)" "yes" \
    "/run/slinit.socket exists"

# list must succeed and contain the boot milestone
_list="$(slinitctl --system list 2>&1)"
assert_exit_code "slinitctl --system list >/dev/null" 0 "list exits 0"
assert_contains "$_list" "boot" "list contains boot milestone"

# boot is itself a milestone-style internal service ("[+]") in the list.
# status on it should report STARTED.
assert_service_state "boot" "STARTED" "boot is STARTED"

# Non-existent service yields an error and a non-zero exit, but does NOT
# wedge the daemon.
assert_exit_code "slinitctl --system status no-such-service-xyz" 1 \
    "status on missing service fails"

# service-dirs must list at least /etc/slinit.d
_dirs="$(slinitctl --system service-dirs 2>&1)"
assert_contains "$_dirs" "/etc/slinit.d" "service-dirs lists /etc/slinit.d"

test_summary
