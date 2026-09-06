# 690-signal-nonexistent-svc — negative path for signal delivery.
# Sends SIGWINCH to a non-existent service; slinitctl returns
# non-zero. Measures error-return latency (svc lookup fails
# early). Compare vs `350` (positive-path signal): delta =
# lookup-only cost minus signal-emit cost.
perf_run_iters "$ITERS" "CtlSignal_missing_svc_negative" \
    "slinitctl signal WINCH nonexistent-svc-$$ || true"
