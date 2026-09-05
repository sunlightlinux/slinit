# 380-slinitctl-setenv-cycle — set + unset an env var on a
# service, per iteration. Exercises the runtime env-mutation
# path (mutex + map update on the service's env, no restart).
# Case-scoped key so cleanup on ITERS>1 is idempotent.
_key="PERF_PROBE_KEY_$$"
perf_run_iters "$ITERS" "CtlSetEnvCycle_socklog" \
    "slinitctl setenv socklog $_key=val && slinitctl unsetenv socklog $_key"
# Belt-and-braces cleanup (in case a run aborted mid-cycle).
slinitctl unsetenv socklog "$_key" > /dev/null 2>&1
