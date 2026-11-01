# 830-openrc-rc-status — OpenRC-style overview. Renders a table
# of loaded services + state. Similar to `slinitctl list` but
# with OpenRC formatting. Delta vs `020-ctl-ls` = shim
# formatting overhead.
# The OpenRC shims are a separate package (slinit-openrc-shims).
if ! command -v rc-status > /dev/null; then
    echo "SKIP: rc-status is not installed (package slinit-openrc-shims)"
    return 0 2>/dev/null || exit 0
fi
perf_run_iters "$ITERS" "RcStatus_overview" "rc-status"
