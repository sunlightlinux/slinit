#!/bin/sh
# Test: `options = shares-console` flag surface.
# Validates:
#   1. The service parses cleanly with the flag set.
#   2. The service reaches STARTED — proves nothing on the boot path
#      chokes on the flag.
#
# Full console-arbitration behavior (a runs-on-console service yielding
# to a shares-console child) would need a two-service dance and a real
# controlling terminal, which the QEMU + busybox harness makes brittle;
# a proper interactive-console test lives in tests/acceptance/ssh.
# This case is a compilation-and-load smoke.

wait_for_service "sc-svc" "STARTED" 10
assert_service_state "sc-svc" "STARTED" "sc-svc STARTED with options=shares-console"

test_summary
