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
#
# Case 900 has since measured the split this header asserts, on v3.2.2,
# and "roughly half" is right: the journal spawn is 1.08ms of a 2.22ms
# status, 49%, and timed on its own it is 1.16ms — a 7% disagreement,
# which is what confirms the attribution rather than it just being the
# plausible candidate.
#
# The cgroup-tree walk, the other extra named above, is **too small for
# case 900 to resolve**: three runs read -0.005, +0.080 and -0.048 ms
# and the sign never settled. So the gap is the fork, and the cgroup
# tree is somewhere under the noise floor rather than a known figure.
# An internal service skips it entirely in any case.
perf_run_iters "$ITERS" "CtlStatus_v7" "slinitctl status boot"
perf_run_iters "$ITERS" "CtlStatus5_v5" "slinitctl status5 boot"
