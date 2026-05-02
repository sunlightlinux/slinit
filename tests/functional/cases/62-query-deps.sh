#!/bin/sh
# Test: query service description, dependents, and dependency graph.
# Validates: status shows description, graph includes relationships.

wait_for_service "query-svc" "STARTED" 10
wait_for_service "query-dep" "STARTED" 10

# Query status — should include description
status=$(slinitctl --system status query-svc 2>&1)
assert_contains "$status" "test service for query" "status shows description"

# Query dependents of query-svc
dependents=$(slinitctl --system dependents query-svc 2>&1)
# dependents output may say "has N dependent(s)" or list them
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$dependents" in
    *query-dep*|*dependent*)
        echo "OK: dependents command returned results for query-svc"
        ;;
    *)
        echo "FAIL: dependents output unexpected: $dependents"
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        ;;
esac

# Verify graph output includes the dependency relationship
graph=$(slinitctl --system graph 2>&1)
assert_contains "$graph" "query-dep" "graph includes query-dep"
assert_contains "$graph" "query-svc" "graph includes query-svc"

# Query status of query-dep — verify it's running with description
dep_status=$(slinitctl --system status query-dep 2>&1)
assert_contains "$dep_status" "dependent of query-svc" "query-dep description shown"

test_summary
