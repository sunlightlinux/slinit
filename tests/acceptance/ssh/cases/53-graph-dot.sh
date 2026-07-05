#!/bin/sh
# 53-graph-dot — DOT-format dependency export.
#
# `slinitctl graph` emits a Graphviz-compatible directed graph of every
# loaded service (cmd/slinitctl/main.go:2531-2667). Expected envelope:
#
#   digraph services {
#     rankdir=LR;
#     node [...]
#     edge [...]
#
#     "svc-name" [shape=... style=filled fillcolor="..." color="..."];
#     ...
#     "from-svc" -> "to-svc" [...];
#     ...
#   }
#
# Build a minimal A→B edge so we can grep for it in the output; assert
# the envelope landmarks too.

SVC_FROM="acceptance-test-graph-from"
SVC_TO="acceptance-test-graph-to"

cleanup() {
    svc_remove "$SVC_FROM" "$SVC_TO"
}
trap cleanup EXIT INT TERM

svc_deploy "$SVC_TO" <<EOF
type = process
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

svc_deploy "$SVC_FROM" <<EOF
type = process
depends-on: $SVC_TO
command = /bin/sh -c 'while :; do sleep 60; done'
restart = false
EOF

slinitctl --system --no-wait start "$SVC_FROM" >/dev/null 2>&1
wait_for_service "$SVC_FROM" "STARTED" 10 || true
wait_for_service "$SVC_TO" "STARTED" 10 || true

_dot=$(slinitctl --system graph 2>&1)

# --- Envelope -------------------------------------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_dot" in
    *"digraph services {"*)
        echo "OK: graph opens with 'digraph services {'"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: missing 'digraph services {' header"
        echo "$_dot" | head -3 | sed 's/^/  | /'
        ;;
esac

# Closing brace on its own final line.
_last=$(echo "$_dot" | grep -v '^[[:space:]]*$' | tail -1)
_TESTS_RUN=$((_TESTS_RUN + 1))
if [ "$_last" = "}" ]; then
    echo "OK: graph closes with '}'"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: expected closing '}', got: '$_last'"
fi

# rankdir hint — proves we're getting the actual DOT preamble, not a
# truncated reply with just `digraph`.
_TESTS_RUN=$((_TESTS_RUN + 1))
case "$_dot" in
    *"rankdir=LR"*)
        echo "OK: DOT preamble includes rankdir=LR"
        ;;
    *)
        _TESTS_FAILED=$((_TESTS_FAILED + 1))
        echo "FAIL: missing rankdir=LR in preamble"
        ;;
esac

# --- Node lines for both endpoints ---------------------------------
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_dot" | grep -qE "\"$SVC_FROM\"[[:space:]]*\["; then
    echo "OK: node line present for $SVC_FROM"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no node line for $SVC_FROM"
fi

_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_dot" | grep -qE "\"$SVC_TO\"[[:space:]]*\["; then
    echo "OK: node line present for $SVC_TO"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: no node line for $SVC_TO"
fi

# --- Edge: SVC_FROM -> SVC_TO --------------------------------------
# The depends-on relation should render as a directed edge.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_dot" | grep -qE "\"$SVC_FROM\"[[:space:]]*->[[:space:]]*\"$SVC_TO\""; then
    echo "OK: edge $SVC_FROM -> $SVC_TO present"
else
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: edge $SVC_FROM -> $SVC_TO missing"
    echo "$_dot" | grep -- '->' | sed 's/^/  | /' | head -10
fi

# --- Negative: no edge in the wrong direction ----------------------
# A dependency is one-directional (SVC_FROM depends on SVC_TO); the
# reverse edge shouldn't appear or the parser/graph builder is wrong.
_TESTS_RUN=$((_TESTS_RUN + 1))
if echo "$_dot" | grep -qE "\"$SVC_TO\"[[:space:]]*->[[:space:]]*\"$SVC_FROM\""; then
    _TESTS_FAILED=$((_TESTS_FAILED + 1))
    echo "FAIL: spurious reverse edge $SVC_TO -> $SVC_FROM"
else
    echo "OK: no spurious reverse edge"
fi

test_summary
