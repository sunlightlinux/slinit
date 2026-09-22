# `docker pause` freezes every process in the container through the
# cgroup freezer; time keeps passing. slinit must come back without
# deciding that a frozen service timed out or that the clock moved
# suspiciously, and still shut down cleanly.

N=slinit-ct-pause-$$
SVC=$(new_svcdir)
trap '"$RUNTIME" unpause $N >/dev/null 2>&1; ct_rm $N; rm -rf "$SVC"' EXIT

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

"$RUNTIME" pause $N >/dev/null
sleep 5
"$RUNTIME" unpause $N >/dev/null
sleep 1

after=$("$RUNTIME" exec $N slinitctl status worker 2>/dev/null | awk '/PID:/ {print $2; exit}')
[ "$after" = "$before" ]
check $? "worker survived the freeze without a restart (pid $before -> ${after:-none})"

ms=$(ct_stop_ms $N 10)
ec=$(ct_exit_code $N)
[ "$ec" = "0" ] && [ "$ms" -lt 3000 ]
check $? "clean stop after unpause (exit $ec, ${ms}ms)"

summary
