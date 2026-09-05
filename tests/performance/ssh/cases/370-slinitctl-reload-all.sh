# 370-slinitctl-reload-all — reload every loaded service from
# disk in a single control call. Heavy read path — server walks
# the full service set, opens + parses each file, updates
# cached configs. Should be roughly N * per-service-reload cost.
perf_run_iters "$ITERS" "CtlReloadAll" "slinitctl reload-all"
