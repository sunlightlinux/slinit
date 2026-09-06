# 830-openrc-rc-status — OpenRC-style overview. Renders a table
# of loaded services + state. Similar to `slinitctl list` but
# with OpenRC formatting. Delta vs `020-ctl-ls` = shim
# formatting overhead.
perf_run_iters "$ITERS" "RcStatus_overview" "rc-status"
