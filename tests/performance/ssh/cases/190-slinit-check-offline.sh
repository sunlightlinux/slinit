# 190-slinit-check-offline — offline config linter latency. Doesn't
# touch the running slinit at all — pure file-read + parse of
# /etc/slinit.d/<svc>. Isolates the pkg/config parser cost from
# the control-socket path. Uses `boot` (small internal svc) as the
# stable target since it's guaranteed to exist on every install.
if ! command -v slinit-check > /dev/null 2>&1; then
    echo "SKIP: slinit-check not installed on target"
    exit 0
fi
perf_run_iters "$ITERS" "SlinitCheckOffline_boot" "slinit-check /etc/slinit.d/boot"
