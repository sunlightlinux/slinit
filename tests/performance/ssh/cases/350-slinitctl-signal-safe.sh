# 350-slinitctl-signal-safe — send SIGWINCH (28) to a running
# service's main pid. SIGWINCH is "window resize" — daemons
# ignore it by default. Measures the signal-delivery code path
# (control socket → svc lookup → kill(2)) without disrupting
# the target service.
#
# By number, not name: slinitctl up to 3.5.0 does not accept the name
# WINCH (its parser knew 9 of the 31 names `signal --list` showed), so
# `signal WINCH` timed a client-side parse error that never reached
# slinit. The number reaches slinit on every version.
perf_run_iters "$ITERS" "CtlSignal_WINCH_perftarget" "slinitctl signal 28 $PERF_TARGET"
