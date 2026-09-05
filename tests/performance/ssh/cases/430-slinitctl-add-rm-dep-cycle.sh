# 430-slinitctl-add-rm-dep-cycle — add a runtime dep + remove it,
# per iteration. Exercises the runtime graph mutation path
# (mutex + graph edge insert/remove). Chosen edge is
# crond→socklog: both stable services, both already loaded, and
# the edge doesn't affect either service's actual dependencies
# (it's a `waits-for` soft edge that both would satisfy anyway).
perf_run_iters "$ITERS" "CtlAddRmDep_crond_wf_socklog" \
    "slinitctl add-dep crond waits-for socklog && slinitctl rm-dep crond waits-for socklog"
# Best-effort cleanup if a run aborted mid-cycle
slinitctl rm-dep crond waits-for socklog > /dev/null 2>&1
