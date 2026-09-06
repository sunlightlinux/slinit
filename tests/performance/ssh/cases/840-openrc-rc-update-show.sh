# 840-openrc-rc-update-show — OpenRC-style enable/disable state
# viewer. Lists services + their auto-start (waits-for-boot)
# state. Read-only equivalent of `slinitctl enable/disable`
# introspection.
perf_run_iters "$ITERS" "RcUpdateShow" "rc-update show"
