# 740-cycle-scripted-start-oneshot — pure `start` hammer on a
# `type=scripted, command=/bin/true` throwaway. Each start fires
# a fresh /bin/true which self-exits immediately, so the svc
# returns to STOPPED between iterations without needing an
# explicit stop. Measures start-alone cost as tightly as
# possible: (start + brief transition + fork/exec /bin/true).
_name="perf-cycle-scr-$$"
_svcfile="/etc/slinit.d/$_name"
printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"
slinitctl load $_name > /dev/null 2>&1 || true

perf_run_iters "$ITERS" "Cycle_Start_Alone_scripted" \
    "slinitctl start $_name"

slinitctl unload $_name > /dev/null 2>&1
rm -f "$_svcfile"
