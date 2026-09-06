# 680-slinitctl-once-throwaway — `once` command starts svc but
# does NOT restart on exit. Same state machine as start except the
# "no-restart" bit is set. Cheap to hammer since command is
# /bin/true (exits immediately).
_name="perf-throwaway-once-$$"
_svcfile="/etc/slinit.d/$_name"
printf "type = scripted\ncommand = /bin/true\n" > "$_svcfile"

perf_run_iters "$ITERS" "CtlOnce_throwaway" \
    "slinitctl once $_name && slinitctl stop $_name"

slinitctl unload "$_name" > /dev/null 2>&1
rm -f "$_svcfile"
