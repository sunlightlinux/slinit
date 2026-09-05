# 290-slinitctl-getallenv-global — dump the global environment
# via control socket. Small map read; should approach the
# CtlVersion baseline (nothing to walk).
perf_run_iters "$ITERS" "CtlGetAllEnvGlobal" "slinitctl getallenv-global"
