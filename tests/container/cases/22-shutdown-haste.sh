# The four degrees of haste in `slinitctl shutdown`, measured against a
# service that ignores SIGTERM and asks for a 10s stop-timeout:
#
#   shutdown halt              wait it out — SIGTERM, stop-timeout, KILL
#   shutdown halt now          kill it at once, then sync/unmount as usual
#   shutdown halt --fast       no teardown at all, but still sync
#   shutdown halt --superfast  the syscall alone, not even a sync
#
# Timing is the assertion because that is the whole point of the
# distinction: the plain form must still honour the stop-timeout the
# service asked for, and the others must not.
#
# Note what this cannot cover. As PID 1 on real hardware, `--fast` goes
# through shutdown.ExecuteForce and `--superfast` through
# ExecuteImmediate, and the difference between them is what they skip
# on the way to the syscall. A container has no syscall to make, so both
# land on the kill path here and look alike. The omissions are pinned by
# TestExecuteImmediateSkipsEverythingButTheSyscall instead.

N=slinit-ct-haste-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
restart = yes
depends-on: stubborn
EOF
cat > "$SVC/stubborn" <<'EOF'
type = process
command = /bin/sh -c "trap '' TERM; while :; do sleep 1; done"
stop-timeout = 10
restart = yes
EOF

# run_shutdown ARGS... — boot a container, ask it to shut down that way,
# print how many milliseconds it took to exit.
run_shutdown() {
    ct_rm $N
    ct_start $N "$SVC"
    ct_wait_ready $N 15 || { echo 0; return 1; }
    _t0=$(date +%s%N)
    "$RUNTIME" exec $N slinitctl shutdown halt "$@" >/dev/null 2>&1
    ct_wait_exit $N 30 || { echo 0; return 1; }
    _t1=$(date +%s%N)
    echo $(((_t1 - _t0) / 1000000))
}

plain=$(run_shutdown)
check $? "plain shutdown completed"
[ "$plain" -ge 8000 ]
check $? "plain shutdown waited out the 10s stop-timeout (${plain}ms)"
[ "$(ct_exit_code $N)" = "0" ]
check $? "plain shutdown exits 0"

now=$(run_shutdown now)
check $? "'now' shutdown completed"
[ "$now" -lt 3000 ]
check $? "'now' killed the service instead of waiting (${now}ms)"
[ "$(ct_exit_code $N)" = "0" ]
check $? "'now' exits 0"
ct_logs $N | grep -qi "killing services immediately"
check $? "the log says the services were killed on request"

fast=$(run_shutdown --fast)
check $? "'--fast' shutdown completed"
[ "$fast" -lt 3000 ]
check $? "'--fast' did not wait either (${fast}ms)"
[ "$(ct_exit_code $N)" = "0" ]
check $? "'--fast' exits 0"

super=$(run_shutdown --superfast)
check $? "'--superfast' shutdown completed"
[ "$super" -lt 3000 ]
check $? "'--superfast' did not wait either (${super}ms)"
[ "$(ct_exit_code $N)" = "0" ]
check $? "'--superfast' exits 0"

# Same hurry through the shortcut spelling.
ct_rm $N
ct_start $N "$SVC"
ct_wait_ready $N 15
t0=$(date +%s%N)
"$RUNTIME" exec $N slinitctl reboot now >/dev/null 2>&1
ct_wait_exit $N 30
check $? "'slinitctl reboot now' completed"
ms=$(( ($(date +%s%N) - t0) / 1000000 ))
[ "$ms" -lt 3000 ]
check $? "the shortcut is as hurried as the long form (${ms}ms)"

# Two different amounts of hurry: asking for both is a contradiction.
ct_rm $N
ct_start $N "$SVC"
ct_wait_ready $N 15
out=$("$RUNTIME" exec $N slinitctl shutdown halt --fast --superfast 2>&1)
[ $? -ne 0 ]
check $? "--fast together with --superfast is refused ($out)"

# The top-level shortcuts take the same arguments. slinitctl.8 promised
# them long before anything implemented them: the dispatcher answered
# "Unknown command: reboot".
"$RUNTIME" exec $N slinitctl reboot --status >/dev/null 2>&1
check $? "'slinitctl reboot --status' is a command, not an error"
"$RUNTIME" exec $N slinitctl halt +5 >/dev/null 2>&1 &&
    "$RUNTIME" exec $N slinitctl shutdown --status 2>/dev/null | grep -qi "halt"
check $? "'slinitctl halt +5' schedules the same way the long form does"
"$RUNTIME" exec $N slinitctl shutdown -c >/dev/null 2>&1
check $? "and it cancels the same way"

# --fast is immediate by definition, so pairing it with a schedule is a
# contradiction the CLI should refuse rather than silently resolve.
ct_rm $N
ct_start $N "$SVC"
ct_wait_ready $N 15
out=$("$RUNTIME" exec $N slinitctl shutdown halt +5 --fast 2>&1)
[ $? -ne 0 ] && echo "$out" | grep -qi "fast"
check $? "scheduling --fast is refused with a reason ($out)"

summary
