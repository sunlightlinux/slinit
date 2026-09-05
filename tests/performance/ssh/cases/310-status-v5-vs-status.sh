# 310-status-v5-vs-status — same shape as `300` but for single-
# service status. `status5` returns extra detail fields; comparing
# vs default `status` isolates the marshalling cost of the extra
# payload.
perf_run_iters "$ITERS" "CtlStatus_v7" "slinitctl status boot"
perf_run_iters "$ITERS" "CtlStatus5_v5" "slinitctl status5 boot"
