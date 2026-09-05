# 470-concurrent-setenv-8 — 8 concurrent setenv on 8 different
# keys of the same target service. Stresses the per-svc env map
# mutex — if lock contention is real here, wall-clock should
# jump well above the serial-setenv baseline (case `380`).
_keys="PERFA_$$ PERFB_$$ PERFC_$$ PERFD_$$ PERFE_$$ PERFF_$$ PERFG_$$ PERFH_$$"
perf_run_iters "$ITERS" "CtlConcurrentSetEnv8" \
    "for _k in $_keys; do slinitctl setenv socklog \"\$_k\"=val > /dev/null & done; wait; \
     for _k in $_keys; do slinitctl unsetenv socklog \"\$_k\" > /dev/null; done"
# Sweep any leftover on abort
for _k in $_keys; do slinitctl unsetenv socklog "$_k" > /dev/null 2>&1; done
