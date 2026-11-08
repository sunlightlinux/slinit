# 740-cycle-scripted-start-oneshot — start + stop on a `type=scripted,
# command=/bin/true` throwaway: the scripted counterpart of 710. A scripted
# service stays STARTED once its command has exited, so a bare `start` only
# does anything the first time; the other iterations measured the "already
# started" reply. Hence the stop in every iteration.
_name="perf-cycle-scr-$$"
_svcfile="/etc/slinit.d/$_name"
printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"
perf_run_iters "$ITERS" "Cycle_Start_Stop_scripted" \
    "slinitctl start $_name && slinitctl stop $_name"
slinitctl unload $_name > /dev/null 2>&1
rm -f "$_svcfile"
