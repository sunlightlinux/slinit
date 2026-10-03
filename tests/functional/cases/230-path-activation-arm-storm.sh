#!/bin/sh
# Test: several path triggers that fire at ARM time, during the load.
#
# start-on-directory-not-empty on an already-non-empty directory fires
# synchronously from pathwatch.arm(), which runs inside the loader's
# OnServiceLoaded hook. main.go's callback then starts the service — on a
# goroutine, which takes the service-graph lock — while the load that
# fired it is still appending edges for the milestone's other
# dependencies.
#
# That overlap was a data race on dependsOn/dependents, measured in
# pkg/config's TestArmTimeFireDuringLoadIsSerialized and fixed by putting
# the boot load under the graph lock. Its direct consequence was a lost
# dependencyStarted() notification — a service that waits forever — which
# is what cases 72 and 181 showed as "no result received".
#
# Three triggers rather than one, because the race needs the start's
# propagation to overlap the load, and one trigger gives one chance per
# boot. Interleaved with ordinary services so there are always more edges
# to append after a trigger has fired.

mark() { echo "MARK: $1" > /dev/console 2>/dev/null || true; }

mark "waiting for the three arm-time services"
wait_for_service "armstorm-a" "STARTED" 15
wait_for_service "armstorm-b" "STARTED" 15
wait_for_service "armstorm-c" "STARTED" 15

assert_service_state "armstorm-a" "STARTED" "armstorm-a started from its arm-time trigger"
assert_service_state "armstorm-b" "STARTED" "armstorm-b started from its arm-time trigger"
assert_service_state "armstorm-c" "STARTED" "armstorm-c started from its arm-time trigger"

# The bodies really ran, so these are processes and not just bookkeeping.
for svc in a b c; do
    _TESTS_RUN=$((_TESTS_RUN + 1))
    if [ -f "/tmp/armstorm-$svc-fired" ]; then
        echo "OK: armstorm-$svc body ran"
    else
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: /tmp/armstorm-$svc-fired missing — the service never executed"
    fi
done

# The graph has to be intact afterwards: every service the milestone
# names must still be there and started, fillers included. A lost append
# or a lost notification shows up as a filler stuck in STARTING.
mark "checking the fillers the load was appending while triggers fired"
for svc in filler-a filler-b filler-c; do
    wait_for_service "$svc" "STARTED" 15
    assert_service_state "$svc" "STARTED" "$svc reached STARTED"
done

# And the control socket still answers, which is the other half of what
# the stall looked like.
mark "checking the control socket still answers"
_TESTS_RUN=$((_TESTS_RUN + 1))
if _list=$(timeout 5 slinitctl --system list 2>/dev/null); then
    echo "OK: slinitctl list answered after the arm-time storm"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: slinitctl list did not answer within 5s — the control socket stalled"
fi

mark "done: summary"
test_summary
