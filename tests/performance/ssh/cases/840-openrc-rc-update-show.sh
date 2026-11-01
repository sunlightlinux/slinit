# 840-openrc-rc-update-show — OpenRC-style enable/disable state
# viewer. Lists services + their auto-start (waits-for-boot)
# state. Read-only equivalent of `slinitctl enable/disable`
# introspection.
# The OpenRC shims are a separate package (slinit-openrc-shims).
if ! command -v rc-update > /dev/null; then
    echo "SKIP: rc-update is not installed (package slinit-openrc-shims)"
    return 0 2>/dev/null || exit 0
fi
perf_run_iters "$ITERS" "RcUpdateShow" "rc-update show"
