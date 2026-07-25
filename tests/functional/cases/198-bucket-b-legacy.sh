#!/bin/sh
# Test: Bucket B legacy directives (coredump-filter, ignore-sigpipe,
# memory-ksm, personality, remove-ipc, timer-slack-nsec) all parse
# and coexist. Each is applied in slinit-runner via a separate small
# code path; the aggregate parse+start smoke catches any of them
# regressing to "unknown setting" or being dropped by the loader.

wait_for_service "bucketb-svc" "STARTED" 10
assert_service_state "bucketb-svc" "STARTED" "Bucket B legacy cluster parses + service starts"

test_summary
