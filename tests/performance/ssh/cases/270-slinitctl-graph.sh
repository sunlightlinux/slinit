# 270-slinitctl-graph — export the full dependency graph in DOT
# format. Walks every service + every dep edge + renders text.
# One of the heavier server-side reads; a slow number here means
# graph traversal or DOT-render is the hot path.
perf_run_iters "$ITERS" "CtlGraph" "slinitctl graph"
