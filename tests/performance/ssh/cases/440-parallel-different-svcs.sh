# 440-parallel-different-svcs — 8 parallel `slinitctl status` on
# 8 DIFFERENT services. Compare wall-clock vs case `060`
# (parallel-status on the same svc). Slower here would point at
# per-service mutex serialising cross-service reads; equal-or-
# faster confirms the ServiceSet's read path is properly parallel.
_svcs="boot socklog dbus udevd elogind crond sshd network"
perf_run_iters "$ITERS" "ParallelStatus8_diff_svcs" \
    "for _s in $_svcs; do slinitctl status \"\$_s\" > /dev/null & done; wait"
