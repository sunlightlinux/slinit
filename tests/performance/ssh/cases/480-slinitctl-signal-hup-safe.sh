# 480-slinitctl-signal-hup-safe — send SIGHUP to the suite's
# perf-target, which traps it, prints a line and keeps running —
# safe to hammer. Exercises the SAME code path as `350` (signal
# delivery) but with a handled signal, so the target's own HUP
# handler shows in wall clock. Delta vs `350` = the HUP handler
# cost, not slinit's. It was socklog until a host without it.
perf_run_iters "$ITERS" "CtlSignal_HUP_perftarget" "slinitctl signal HUP $PERF_TARGET"
