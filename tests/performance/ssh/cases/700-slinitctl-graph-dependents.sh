# 700-slinitctl-graph-dependents — compound: full graph export
# + dependents-of-boot in the same shell round-trip. Simulates
# a graph-inspection tool doing both queries back-to-back.
# Reveals whether the server can share intermediate state
# between the two walks (would show as sub-linear total cost)
# or does each walk from scratch.
perf_run_iters "$ITERS" "CtlGraphPlusDependents_boot" \
    "slinitctl graph > /dev/null && slinitctl dependents boot"
