# 410-slinitctl-load-mech — loader-mechanism info query. Cheap
# per-op; floor check for a code path that rarely runs (mostly a
# debug/diagnostic tool).
perf_run_iters "$ITERS" "CtlLoadMech" "slinitctl load-mech"
