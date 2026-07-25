#!/bin/sh
# Test: miscellaneous coverage — four small svcs, one directive-family
# each, all folded into a single test to keep the count from blowing up:
#   cronxtra-svc  → cron-persistent + cron-randomized-delay
#   sockperm-svc  → socket-uid + socket-gid (permissions covered by 132)
#   bindro-svc    → bind-read-only-paths (bind-paths covered by 78)
#   kw-svc        → keyword platform gate (kvm matches the QEMU VM)

wait_for_service "cronxtra-svc" "STARTED" 10
assert_service_state "cronxtra-svc" "STARTED" "cron-persistent + cron-randomized-delay parses"

wait_for_service "sockperm-svc" "STARTED" 10
assert_service_state "sockperm-svc" "STARTED" "socket-uid + socket-gid parses"

wait_for_service "bindro-svc" "STARTED" 10
assert_service_state "bindro-svc" "STARTED" "bind-read-only-paths parses"

wait_for_service "kw-svc" "STARTED" 10
assert_service_state "kw-svc" "STARTED" "keyword = kvm matches the QEMU VM platform"

test_summary
