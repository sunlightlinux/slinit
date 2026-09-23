# The three degrees of haste in `slinitctl shutdown`, measured against a
# service that ignores SIGTERM and asks for a 10s stop-timeout:
#
#   shutdown halt          wait it out — SIGTERM, stop-timeout, SIGKILL
#   shutdown halt now      kill it at once, then sync/unmount as usual
#   shutdown halt --fast   no teardown at all
#
# Timing is the assertion because that is the whole point of the
# distinction: the plain form must still honour the stop-timeout the
# service asked for, and the other two must not.
#
# Note what this cannot cover: `--fast` as PID 1 on real hardware goes
# through shutdown.ExecuteForce — sync and the reboot syscall, no
# unmount. In a container there is no syscall to make, so it lands on
# the kill path instead. The bare-metal branch is exercised by
# reboot(8) -f, not from here.

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

# --fast is immediate by definition, so pairing it with a schedule is a
# contradiction the CLI should refuse rather than silently resolve.
ct_rm $N
ct_start $N "$SVC"
ct_wait_ready $N 15
out=$("$RUNTIME" exec $N slinitctl shutdown halt +5 --fast 2>&1)
[ $? -ne 0 ] && echo "$out" | grep -qi "fast"
check $? "scheduling --fast is refused with a reason ($out)"

summary
