# 480-slinitctl-signal-hup-safe — send SIGHUP to socklog.
# socklog re-opens its output on HUP (config-reload behaviour)
# but keeps running — safe to hammer. Exercises the SAME code
# path as `350` (signal delivery) but with a non-ignored signal,
# so downstream cost (socklog's own HUP handler) shows in wall
# clock. Delta vs `350` = the HUP handler cost, not slinit's.
perf_run_iters "$ITERS" "CtlSignal_HUP_socklog" "slinitctl signal HUP socklog"
