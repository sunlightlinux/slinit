# 20-ctl-ls — `slinitctl ls`: opens control socket, requests the
# full service list (one packet per service), decodes + prints.
# Measures socket-connect + serial N-service round-trip.
perf_run_iters "$ITERS" "CtlLs" "slinitctl ls"
