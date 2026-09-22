# SIGHUP reaches PID 1 easily — a detaching terminal, a stray `kill -1`,
# a process manager tidying up. slinit claims it (an unclaimed SIGHUP
# would be fatal under the Go runtime) but must not act on it: nothing
# stops, and the container keeps running.

N=slinit-ct-hup-$$
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
"$RUNTIME" kill -s HUP $N >/dev/null
sleep 2

[ "$("$RUNTIME" inspect -f '{{.State.Running}}' $N)" = "true" ]
check $? "container still running after SIGHUP"

after=$("$RUNTIME" exec $N slinitctl status worker 2>/dev/null | awk '/PID:/ {print $2; exit}')
[ "$after" = "$before" ]
check $? "worker untouched by SIGHUP (pid $before -> ${after:-none})"

ms=$(ct_stop_ms $N 10)
[ "$(ct_exit_code $N)" = "0" ]
check $? "SIGTERM afterwards still stops it cleanly (${ms}ms)"

summary
