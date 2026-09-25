# 430-slinitctl-add-rm-dep-cycle — add a runtime dep + remove it,
# per iteration. Exercises the runtime graph mutation path
# (mutex + graph edge insert/remove). Chosen edge is
# crond→socklog: both stable services, both already loaded, and
# the edge doesn't affect either service's actual dependencies
# (it's a `waits-for` soft edge that both would satisfy anyway).
perf_run_iters "$ITERS" "CtlAddRmDep_crond_wf_socklog" \
    "slinitctl add-dep crond waits-for socklog && slinitctl rm-dep crond waits-for socklog"
# Best-effort cleanup if a run aborted mid-cycle.
#
# `|| true` is load-bearing, not tidiness. The loop above already
# removed the edge on its last iteration, so this rm-dep fails every
# single run. Redirecting output hides the message but NOT the exit
# status, and this is the last command in the file — so it became the
# case's status, and run.sh's `set -e` killed the suite right here.
# That silently cut the run at case 46 of 92.
slinitctl rm-dep crond waits-for socklog > /dev/null 2>&1 || true
