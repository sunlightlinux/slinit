# 240-mixed-heavy-load — heterogeneous fan-out: 16 status + 8
# journalctl + 4 boot-time all firing at once. Realistic peak
# admin session shape (multiple tail -f terminals, monitoring
# hooks polling status). Wall clock = worst-case cost when the
# whole surface is exercised simultaneously.
perf_run_iters "$ITERS" "MixedHeavyLoad_28parallel" '
    _i=0; while [ $_i -lt 16 ]; do slinitctl status boot > /dev/null 2>&1 & _i=$((_i+1)); done
    _i=0; while [ $_i -lt 8  ]; do slinit-journalctl -n 100 > /dev/null 2>&1 & _i=$((_i+1)); done
    _i=0; while [ $_i -lt 4  ]; do slinitctl boot-time > /dev/null 2>&1 & _i=$((_i+1)); done
    wait'
