# 70-ctl-status-every-svc — status on EACH of the currently-loaded
# services (name list from `slinitctl ls`). Total wall-clock ÷ N =
# effective per-service status cost when queried serially across
# the whole set — reveals worst-case service if any one is
# unusually slow (e.g. a service whose state read touches
# unbounded state).
_svcs="$(slinitctl ls 2>/dev/null | awk '{for(i=1;i<=NF;i++) if ($i !~ /^\[|^\]|^\{|^\(pid:|^[0-9]+\)$/) { print $i; break } }' | sort -u | tr '\n' ' ')"
if [ -z "$_svcs" ]; then
    echo "FAIL: could not enumerate services from slinitctl ls" >&2
    exit 1
fi
perf_run_iters "$ITERS" "CtlStatusAllSvc" \
    "for _s in $_svcs; do slinitctl status \"\$_s\" > /dev/null; done"
