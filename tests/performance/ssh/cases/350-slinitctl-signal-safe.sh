# 350-slinitctl-signal-safe — send SIGWINCH (28) to a running
# service's main pid. SIGWINCH is "window resize" — daemons
# ignore it by default. Measures the signal-delivery code path
# (control socket → svc lookup → kill(2)) without disrupting
# the target service.
perf_run_iters "$ITERS" "CtlSignal_WINCH_socklog" "slinitctl signal WINCH socklog"
