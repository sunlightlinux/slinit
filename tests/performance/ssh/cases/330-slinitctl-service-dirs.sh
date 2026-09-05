# 330-slinitctl-service-dirs — dump the configured service
# directories. Trivial-work read path; should sit at the
# baseline. Useful as a floor check for "is this specific
# code path anomalous".
perf_run_iters "$ITERS" "CtlServiceDirs" "slinitctl service-dirs"
