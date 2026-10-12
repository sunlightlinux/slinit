#!/bin/sh
# 241-control-concurrency-storm — hammer the MUTATING control verbs from
# many connections at once and require PID 1 to survive.
#
# WHAT THIS DOES AND DOES NOT CATCH — measured, because the obvious
# reading is wrong.
#
# It drives eight concurrent clients through the real control protocol
# and requires PID 1 to be the same process afterwards and still
# answering. As a smoke test for daemon survival under churn that is
# worth having: it is the only case that exercises the protocol from
# many connections at once, so it would catch a deadlock, a wedged
# dependency graph, or a crash from any cause.
#
# It is NOT a guard against the map-race class, and that was checked
# rather than assumed. Three of slinit's four recorded kernel panics
# were one bug — shared state on a daemon singleton, mutated from one
# goroutine per control connection, with no lock (`DirLoader.loading`
# cfe16ab, the per-service `extraEnv` map b7faaad, `AddDep` 983bfa2).
# Each was a Go runtime `fatal error: concurrent map read and map
# write`, unrecoverable, and slinit is PID 1, so the kernel panicked.
#
# This case was written to catch that class and cannot. With
# `ServiceSet.envMu` deliberately stripped out, it ran twelve seconds
# with eight workers and passed every assertion. The reason is
# dimensional: one `slinitctl` invocation is a fork, an exec, a connect
# and a round-trip, so a shell loop produces a few hundred operations
# that almost never overlap inside the daemon. Process spawn dominates;
# contention is microscopic.
#
# The real guards are Go-level, where contention can be manufactured
# where it actually happens: pkg/service/globalenv_race_test.go,
# env_race_test.go, depgraph_race_test.go and
# pkg/config/loader_concurrent_test.go. The global-env one fails in
# milliseconds against the same mutation this case survived.
#
STORM_SECS="${STORM_SECS:-12}"

_pid1_start_before=$(awk '{print $22}' /proc/1/stat 2>/dev/null)
echo "INFO: PID 1 starttime before: $_pid1_start_before"

# Targets for the dependency churn. Loaded up front so the churn is
# about the graph, not about loading.
for i in 1 2 3 4; do
    cat > /etc/slinit.d/storm-target-$i <<EOF
type = internal
EOF
    slinitctl --system load storm-target-$i > /dev/null 2>&1
done

_end=$(( $(date +%s) + STORM_SECS ))

# Each worker is a different mix, so the verbs interleave rather than
# queueing behind one hot path.

# 1-2: global environment — the extraEnv race (b7faaad).
for w in 1 2; do
    (
        n=0
        while [ "$(date +%s)" -lt "$_end" ]; do
            n=$((n + 1))
            slinitctl --system setenv "STORM_${w}_$((n % 8))=v$n" > /dev/null 2>&1
            slinitctl --system unsetenv "STORM_${w}_$(((n + 3) % 8))" > /dev/null 2>&1
            slinitctl --system getallenv-global > /dev/null 2>&1
        done
    ) &
done

# 3-4: dependency graph — the AddDep race (983bfa2).
for w in 3 4; do
    (
        while [ "$(date +%s)" -lt "$_end" ]; do
            a=$(( (w % 4) + 1 ))
            b=$(( ((w + 1) % 4) + 1 ))
            slinitctl --system add-dep waits-for boot storm-target-$a > /dev/null 2>&1
            slinitctl --system rm-dep waits-for boot storm-target-$a > /dev/null 2>&1
            slinitctl --system add-dep waits-for boot storm-target-$b > /dev/null 2>&1
            slinitctl --system rm-dep waits-for boot storm-target-$b > /dev/null 2>&1
        done
    ) &
done

# 5-6: load/unload of DISTINCT services — the DirLoader.loading race
# (cfe16ab). Distinct names matter: the early return in LoadService
# skips the loader entirely for a service already in the set.
for w in 5 6; do
    (
        n=0
        while [ "$(date +%s)" -lt "$_end" ]; do
            n=$((n + 1))
            svc="storm-churn-${w}-${n}"
            printf 'type = scripted\ncommand = /bin/true\nmanual = yes\n' > "/etc/slinit.d/$svc"
            slinitctl --system load "$svc" > /dev/null 2>&1
            slinitctl --system unload "$svc" > /dev/null 2>&1
            rm -f "/etc/slinit.d/$svc"
        done
    ) &
done

# 7: readers. They were part of the dep-graph bug, not bystanders.
(
    while [ "$(date +%s)" -lt "$_end" ]; do
        slinitctl --system list > /dev/null 2>&1
        slinitctl --system status boot > /dev/null 2>&1
        slinitctl --system show boot > /dev/null 2>&1
        slinitctl --system service-dirs > /dev/null 2>&1
    done
) &

# 8: the rest of the mutating surface.
(
    while [ "$(date +%s)" -lt "$_end" ]; do
        slinitctl --system reset-failed > /dev/null 2>&1
        slinitctl --system start storm-target-1 > /dev/null 2>&1
        slinitctl --system stop --ignore-unstarted storm-target-1 > /dev/null 2>&1
        slinitctl --system boot-time > /dev/null 2>&1
    done
) &

wait
echo "INFO: storm finished (${STORM_SECS}s, 8 concurrent workers)"

# --- the assertions ---

# PID 1 must be the same process. A Go fatal kills it; in the VM that is
# a panic, but if the harness ever runs slinit as a child this catches a
# silent restart too.
_pid1_start_after=$(awk '{print $22}' /proc/1/stat 2>/dev/null)
echo "INFO: PID 1 starttime after:  $_pid1_start_after"
assert_eq "$_pid1_start_after" "$_pid1_start_before" \
    "PID 1 is the same process it was before the storm"

# And it must still answer, not merely exist.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinitctl --system list > /dev/null 2>&1; then
    echo "OK: the control socket still answers after the storm"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: the daemon stopped answering"
fi

# A Go runtime fatal prints to stderr before dying. If anything of the
# kind reached the console or the journal, say so by name — a surviving
# PID 1 with a recorded fatal would mean the harness, not slinit, got
# lucky.
_fatal=$( (slinit-journalctl -n 400 --no-pager 2>/dev/null; cat /var/log/slinit.log 2>/dev/null) \
    | grep -ciE "fatal error|concurrent map|DATA RACE" )
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "${_fatal:-0}" -eq 0 ]; then
    echo "OK: no runtime fatal or concurrent-map report anywhere"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: $_fatal runtime-fatal lines found"
fi

# The storm must not have left the graph wedged: boot still resolves and
# the targets are still there to be queried.
_TESTS_RUN=$((_TESTS_RUN + 1))
if slinitctl --system status storm-target-1 > /dev/null 2>&1; then
    echo "OK: the dependency churn left the graph usable"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: a storm target is no longer queryable"
fi

for i in 1 2 3 4; do
    slinitctl --system --ignore-unstarted stop storm-target-$i > /dev/null 2>&1
    slinitctl --system unload storm-target-$i > /dev/null 2>&1
    rm -f /etc/slinit.d/storm-target-$i
done
rm -f /etc/slinit.d/storm-churn-* 2>/dev/null

test_summary
