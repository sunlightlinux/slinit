# 430-slinitctl-add-rm-dep-cycle — add a runtime dep + remove it,
# per iteration. Exercises the runtime graph mutation path
# (mutex + graph edge insert/remove). The edge is between the
# suite's two fixture services, both started: a `waits-for` soft
# edge the target already satisfies. It was crond→socklog until a
# host without either turned it into a timing of the error reply.
perf_run_iters "$ITERS" "CtlAddRmDep_perftarget_wf_dep" \
    "slinitctl add-dep $PERF_TARGET waits-for $PERF_TARGET_DEP && slinitctl rm-dep $PERF_TARGET waits-for $PERF_TARGET_DEP"
# Best-effort cleanup if a run aborted mid-cycle.
#
# `|| true` is load-bearing, not tidiness. The loop above already
# removed the edge on its last iteration, so this rm-dep fails every
# single run. Redirecting output hides the message but NOT the exit
# status, and this is the last command in the file — so it became the
# case's status, and run.sh's `set -e` killed the suite right here.
# That silently cut the run at case 46 of 92.
slinitctl rm-dep "$PERF_TARGET" waits-for "$PERF_TARGET_DEP" > /dev/null 2>&1 || true
