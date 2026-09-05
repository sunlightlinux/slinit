# 320-slinitctl-catlog — read a service's buffered stdout log
# via control socket. Read path spans slinit's ring buffer +
# response streaming. Uses socklog as a stable target — always
# present + relatively quiet.
perf_run_iters "$ITERS" "CtlCatLog_socklog" "slinitctl catlog socklog"
