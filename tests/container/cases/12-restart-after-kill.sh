# A service killed from outside — `docker exec ... kill -9`, an OOM
# kill, a crash — comes back when it asks to, and the container keeps
# running while that happens.
#
# Both the service and the boot target need `restart = yes`. dinit's
# rule, which slinit follows: a service restarts when it stops
# unexpectedly *or when a dependency stops*. Leave it off the boot
# target and the target stays down after the dependency dies, which
# ends the container (case 13).

N=slinit-ct-restart-$$
SVC=$(new_svcdir)
trap 'ct_rm $N; rm -rf "$SVC"' EXIT

cat > "$SVC/boot" <<'EOF'
type = internal
restart = yes
depends-on: worker
EOF
cat > "$SVC/worker" <<'EOF'
type = process
command = /bin/sh -c "while :; do sleep 1; done"
restart = yes
EOF

ct_start $N "$SVC"
ct_wait_ready $N 10
check $? "boot service reached STARTED"

before=$("$RUNTIME" exec $N slinitctl status worker | awk '/PID:/ {print $2; exit}')
"$RUNTIME" exec $N kill -9 "$before"

after=""
i=0
while [ $i -lt 40 ]; do
    after=$("$RUNTIME" exec $N slinitctl status worker 2>/dev/null | awk '/PID:/ {print $2; exit}')
    [ -n "$after" ] && [ "$after" != "$before" ] && break
    sleep 0.25
    i=$((i + 1))
done

[ -n "$after" ] && [ "$after" != "$before" ]
check $? "worker restarted after SIGKILL (pid $before -> ${after:-none})"

[ "$("$RUNTIME" inspect -f '{{.State.Running}}' $N)" = "true" ]
check $? "container stayed up through the restart"

ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ]; check $? "still stops cleanly afterwards (exit $ec, ${ms}ms)"

summary
