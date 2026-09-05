# 280-slinitctl-dependents — reverse-dep query. Server walks the
# full dep graph looking for edges pointing at <svc>. Same walk
# as graph export but filtered — should be equal or faster.
perf_run_iters "$ITERS" "CtlDependents_boot" "slinitctl dependents boot"
