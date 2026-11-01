# 470-concurrent-setenv-8 — 8 concurrent setenv on 8 different
# keys of the same target service. Stresses the per-svc env map
# mutex — if lock contention is real here, wall-clock should
# jump well above the serial-setenv baseline (case `380`).
#
# Each setenv is waited for by pid: a bare `wait` returns 0 whatever its
# jobs returned, and on a host without socklog (the target until then)
# all eight failed unseen. Only the cleanup's exit status reached the
# suite, which is how it was found.
_keys="PERFA_$$ PERFB_$$ PERFC_$$ PERFD_$$ PERFE_$$ PERFF_$$ PERFG_$$ PERFH_$$"
perf_run_iters "$ITERS" "CtlConcurrentSetEnv8" '
    _pids=
    for _k in $_keys; do slinitctl setenv "$PERF_TARGET" "$_k=val" > /dev/null & _pids="$_pids $!"; done
    _ok=0
    for _q in $_pids; do wait $_q || _ok=1; done
    for _k in $_keys; do slinitctl unsetenv "$PERF_TARGET" "$_k" > /dev/null || _ok=1; done
    [ $_ok = 0 ]'
# Sweep any leftover on abort. `|| true`: these keys were already unset
# above, so each unsetenv fails, and the case would exit with it.
for _k in $_keys; do slinitctl unsetenv "$PERF_TARGET" "$_k" > /dev/null 2>&1 || true; done
