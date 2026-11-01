# 440-parallel-different-svcs — 8 parallel `slinitctl status` on
# 8 DIFFERENT services. Compare wall-clock vs case `060`
# (parallel-status on the same svc). Slower here would point at
# per-service mutex serialising cross-service reads; equal-or-
# faster confirms the ServiceSet's read path is properly parallel.
#
# Every name here must be a service that actually loads; a missing one
# times slinitctl's error path instead of a status read. A fixed list
# kept going stale (`elogind` → `logind`, then a reinstall without
# socklog, dbus, logind or crond), so the 8 are taken from what the
# target has loaded, as 070 does. Each job is waited for by pid: a bare
# `wait` returns 0 whatever its jobs returned, which is how the missing
# names went unnoticed.
_svcs="$(slinitctl ls 2>/dev/null | awk '{for(i=1;i<=NF;i++) if ($i !~ /^\[|^\]|^\{|^\(pid:|^[0-9]+\)$/) { print $i; break } }' | sort -u | head -8 | tr '\n' ' ')"
if [ "$(echo $_svcs | wc -w)" -lt 8 ]; then
    echo "FAIL: fewer than 8 services loaded on the target: $_svcs" >&2
    exit 1
fi
perf_run_iters "$ITERS" "ParallelStatus8_diff_svcs" '
    _pids=
    for _s in $_svcs; do slinitctl status "$_s" > /dev/null & _pids="$_pids $!"; done
    _ok=0
    for _q in $_pids; do wait $_q || _ok=1; done
    [ $_ok = 0 ]'

