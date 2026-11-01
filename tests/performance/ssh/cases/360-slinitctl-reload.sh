# 360-slinitctl-reload — re-read service config from disk.
# Idempotent (config unchanged → no observable effect on the
# service) but exercises the load-service code path a start
# wouldn't hit again once cached. Targets the suite's own
# perf-target: socklog, used until then, is not on every install.
perf_run_iters "$ITERS" "CtlReload_perftarget" "slinitctl reload $PERF_TARGET"
