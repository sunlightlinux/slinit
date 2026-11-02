# 350-slinitctl-signal-safe — send SIGWINCH (28) to a running
# service's main pid. SIGWINCH is "window resize" — daemons
# ignore it by default. Measures the signal-delivery code path
# (control socket → svc lookup → kill(2)) without disrupting
# the target service.
#
# By number, not name, so the case works against any target: before
# 3.6.0 slinitctl rejected the name WINCH (its parser knew 9 of the 31
# names `signal --list` showed), and this case timed that client-side
# error without reaching slinit.
perf_run_iters "$ITERS" "CtlSignal_WINCH_perftarget" "slinitctl signal 28 $PERF_TARGET"
