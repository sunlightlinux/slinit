# 540-throwaway-pause-continue — pause + continue cycle on a
# long-running throwaway `type=process, command=/bin/sleep 3600`
# service. Exercises the SIGSTOP/SIGCONT delivery path via
# slinitctl (short of restart — the process keeps its pid).
# Round-trip = fetch pid + kill(SIGSTOP) + fetch pid + kill(SIGCONT).
_name="perf-throwaway-pause-$$"
_svcfile="/etc/slinit.d/$_name"
printf "type = process\ncommand = /bin/sleep 3600\nrestart = no\n" > "$_svcfile"
slinitctl start "$_name" > /dev/null 2>&1
sleep 0.3   # let the sleep process actually spawn

perf_run_iters "$ITERS" "ServicePauseContinueCycle" \
    "slinitctl pause $_name > /dev/null 2>&1; slinitctl continue $_name > /dev/null 2>&1"

# Teardown
slinitctl stop "$_name"   > /dev/null 2>&1
slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
