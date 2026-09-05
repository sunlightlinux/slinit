# 390-slinitctl-getallenv-svc — per-service env dump. Similar
# shape to `290`'s global-env dump but reads the per-service
# overlay. Slower would point at env-inheritance-walk cost.
perf_run_iters "$ITERS" "CtlGetAllEnv_socklog" "slinitctl getallenv socklog"
