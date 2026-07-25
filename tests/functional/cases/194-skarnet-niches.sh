#!/bin/sh
# Test: skarnet-inspired niches — alert-file/-level, pass-cs-fd,
# utmp-mode all parse and coexist. These directives target specialized
# setups (embedded status displays, controlled fd hand-off, TTY session
# accounting) that don't lend themselves to a quick VM check; the
# regression we want to catch is any of them being silently dropped
# by the loader.

wait_for_service "skarnet-svc" "STARTED" 10
assert_service_state "skarnet-svc" "STARTED" "skarnet-niches cluster parses + service starts"

test_summary
