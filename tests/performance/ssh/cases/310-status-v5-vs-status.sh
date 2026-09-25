# 310-status-v5-vs-status — same shape as `300` but for single-
# service status.
#
# This does NOT isolate marshalling cost, and the delta is not the v5
# wire being cheaper. Both commands do exactly one handle lookup, one
# command and one reply. The difference is entirely client-side and
# unconditional (not behind -l): `status` also walks the cgroup tree
# and calls printRecentJournal, which fork+execs a whole
# slinit-journalctl. `status5` does neither.
#
# So the ~2x gap measured here (v7 2.28ms vs v5 1.11ms on ceres,
# v2.4.2) is a subprocess spawn, not slinit's control path. Same
# caveat applies to every other `status` number in this suite — 030,
# 070, 150, 250 are all roughly half journalctl fork. Use `status5`
# as the baseline when what you want to measure is the ServiceSet
# read path.
perf_run_iters "$ITERS" "CtlStatus_v7" "slinitctl status boot"
perf_run_iters "$ITERS" "CtlStatus5_v5" "slinitctl status5 boot"
