# 360-slinitctl-reload — re-read service config from disk.
# Idempotent (config unchanged → no observable effect on the
# service) but exercises the load-service code path a start
# wouldn't hit again once cached. Uses socklog as the target —
# stable + present on every install.
perf_run_iters "$ITERS" "CtlReload_socklog" "slinitctl reload socklog"
