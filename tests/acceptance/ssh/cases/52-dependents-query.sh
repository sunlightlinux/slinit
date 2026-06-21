#!/bin/sh
# 52-dependents-query — inverse-dependency lookup.
#
# `slinitctl dependents SVC` walks the reverse-edge list and prints every
# service that hard-depends on SVC. Output format (cmd/slinitctl/main.go:2497,
# 2501, 2520):
#
#   Service 'NAME' has no dependents.
#   Service 'NAME' has N dependent(s):
#     dep-name-1
#     dep-name-2
#
# Build a tiny dep DAG and probe both branches: a leaf (no dependents) and
# a service with two distinct hard-dependents.

SVC_BASE="acceptance-test-dep-base"
SVC_A="acceptance-test-dep-a"
SVC_B="acceptance-test-dep-b"
SVC_LEAF="acceptance-test-dep-leaf"

cleanup() {
    svc_remove "$SVC_A" "$SVC_B" "$SVC_BASE" "$SVC_LEAF"
}
trap cleanup EXIT INT TERM

# --- Build the DAG --------------------------------------------------
# SVC_BASE has two reverse-edges: SVC_A and SVC_B both `depends-on` it.
# SVC_LEAF is unrelated (no dependents).
svc_deploy "$SVC_BASE" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

svc_deploy "$SVC_A" <<EOF
type = process
depends-on: $SVC_BASE
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

svc_deploy "$SVC_B" <<EOF
type = process
depends-on: $SVC_BASE
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

svc_deploy "$SVC_LEAF" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

# `dependents` walks the reverse-edge list and does NOT require services
# to be running. Starting them is just to make sure the dependent
# records are fully wired in slinit's registry.
slinitctl --system --no-wait start "$SVC_A" >/dev/null 2>&1
slinitctl --system --no-wait start "$SVC_B" >/dev/null 2>&1
slinitctl --system --no-wait start "$SVC_LEAF" >/dev/null 2>&1
wait_for_service "$SVC_A" "STARTED" 10 || true
wait_for_service "$SVC_B" "STARTED" 10 || true
wait_for_service "$SVC_LEAF" "STARTED" 10 || true

# --- Probe 1: SVC_BASE has two dependents --------------------------
_out=$(slinitctl --system dependents "$SVC_BASE" 2>&1)

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *"has 2 dependent(s)"*)
        echo "OK: dependents header reports count=2"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: expected 'has 2 dependent(s)' header, got:"
        echo "$_out" | sed 's/^/  | /'
        ;;
esac

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    *"$SVC_A"*"$SVC_B"*|*"$SVC_B"*"$SVC_A"*)
        echo "OK: both $SVC_A and $SVC_B listed as dependents of $SVC_BASE"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: dependents list missing $SVC_A or $SVC_B:"
        echo "$_out" | sed 's/^/  | /'
        ;;
esac

# --- Probe 2: SVC_LEAF has no dependents ---------------------------
_out_leaf=$(slinitctl --system dependents "$SVC_LEAF" 2>&1)

_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out_leaf" in
    *"has no dependents"*)
        echo "OK: leaf service reports 'no dependents'"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: expected 'has no dependents' for $SVC_LEAF, got:"
        echo "$_out_leaf" | sed 's/^/  | /'
        ;;
esac

# --- Probe 3: header name matches the queried service --------------
# The `Service 'NAME' has ...` header should quote the queried name
# verbatim — protects against off-by-one / handle-swap bugs.
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_out" in
    "Service '$SVC_BASE'"*)
        echo "OK: header quotes the queried service name"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: header doesn't quote '$SVC_BASE' verbatim:"
        echo "$_out" | head -1 | sed 's/^/  | /'
        ;;
esac

test_summary
