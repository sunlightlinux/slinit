# 30-ctl-status — `slinitctl status <svc>`: single-service state +
# dep tree walk. `boot` is the aggregate target so its status forces
# slinit to gather info on every dependency.
perf_run_iters "$ITERS" "CtlStatus_boot" "slinitctl status boot"
