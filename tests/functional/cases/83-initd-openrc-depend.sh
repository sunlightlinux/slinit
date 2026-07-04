#!/bin/sh
# Test: /etc/init.d auto-detect handles OpenRC-style depend() functions.
# Validates: initd.go fallback triggers on openrc-run shebang when LSB is
# absent, sandbox `sh -c` extracts need/after into DependsOn/After, and
# the resulting slinit service inherits the correct dependency graph.

wait_for_service "boot" "STARTED" 10

# The init.d fallback discovers /etc/init.d/openrc-svc but does NOT
# auto-start it (init.d imports are always manual= for safety). We
# start it explicitly and rely on the parsed `need target-svc` to
# pull target-svc into the graph first.

# Precondition: target-svc must NOT be running yet — otherwise a
# passing test would prove nothing about the depend() extraction.
_TESTS_RUN=$((_TESTS_RUN + 1))
initial_target=$(slinitctl --system status target-svc 2>&1 || true)
case "$initial_target" in
    *STOPPED*)
        echo "OK: target-svc starts out STOPPED"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: target-svc should be STOPPED, saw: $initial_target"
        ;;
esac

# Now start the openrc-svc — depend() said `need target-svc`, so
# slinit must bring target-svc up first before openrc-svc runs.
slinitctl --system start openrc-svc 2>&1
sleep 2

# openrc-svc's start block wrote to /tmp/openrc-svc-status.
assert_eq "$(cat /tmp/openrc-svc-status 2>/dev/null)" "openrc-svc: started" \
    "openrc-svc start command executed"

# target-svc must have been started transitively (need = hard dep).
assert_eq "$(cat /tmp/target-svc-status 2>/dev/null)" "target-svc started" \
    "target-svc auto-started via depend() need directive"

# Both should be in STARTED state.
assert_service_state "target-svc" "STARTED" "target-svc is STARTED"
assert_service_state "openrc-svc" "STARTED" "openrc-svc is STARTED"

# graph view should show the dep edge.
graph=$(slinitctl --system graph 2>&1)
assert_contains "$graph" "openrc-svc" "graph includes openrc-svc"
assert_contains "$graph" "target-svc" "graph includes target-svc"

# Stop openrc-svc — verify its stop function runs. dinit propagates
# reverse deps on stop by default, so target-svc may follow it down;
# that behaviour is exercised by other cases and is not what this
# test is asserting.
slinitctl --system stop openrc-svc 2>&1
sleep 1
assert_eq "$(cat /tmp/openrc-svc-status 2>/dev/null)" "openrc-svc: stopped" \
    "openrc-svc stop command executed"

test_summary
