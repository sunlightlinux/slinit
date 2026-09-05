# 40-ctl-boot-time — `slinitctl boot-time`: server-side aggregation
# of per-service start timings + formatted print. Measures the
# heavier read path (all-services scan + text render).
perf_run_iters "$ITERS" "CtlBootTime" "slinitctl boot-time"
