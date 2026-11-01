# 320-slinitctl-catlog — read a service's buffered stdout log
# via control socket. Read path spans slinit's ring buffer +
# response streaming. Targets the suite's own perf-target, which
# has log-type = buffer; socklog was used until a host without it
# turned this into a timing of the "could not be loaded" reply.
perf_run_iters "$ITERS" "CtlCatLog_perftarget" "slinitctl catlog $PERF_TARGET"
